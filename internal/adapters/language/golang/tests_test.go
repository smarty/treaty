package golang

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/smarty/treaty/internal/app"
)

// writeTree writes files under a new temporary directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

var mathTree = map[string]string{
	"go.mod": "module example.com/calc\n\ngo 1.22\n",
	"math/math.go": `package math

func Add(a, b int) int {
	return a + b
}

func Sub(a, b int) int {
	if a < b {
		return -(b - a)
	}

	return a - b
}

type Meter struct{ total int }

func (this *Meter) Count() int {
	this.total++
	return this.total
}
`,
	"math/math_test.go": `package math

import "testing"

func TestAdd(t *testing.T) {
	if sum(2, 3) != 5 {
		t.Fatal("sum")
	}
}

func TestMeter_Count(t *testing.T) {
	t.Run("once", func(t *testing.T) {})
}

func TestBroken(t *testing.T) {
	t.Fatal("broken on purpose")
}

func sum(a, b int) int { return Add(a, b) }

func helperNotATest() {}

func Testable() bool { return true }
`,
	"math/external_test.go": `package math_test

import (
	"testing"

	"example.com/calc/math"
)

type fixture struct{}

func (this *fixture) check(t *testing.T) { _ = math.Sub(3, 1) }

func TestExternal(t *testing.T) { new(fixture).check(t) }
`,
}

func TestDiscoverFindsTestsAndWhatTheyUse(t *testing.T) {
	root := writeTree(t, mathTree)
	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	cases, err := NewTestSuite().Discover(root, g)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]app.TestCase{}
	for _, each := range cases {
		found[each.Name] = each
	}

	if len(found) != 4 || found["Testable"].Name != "" {
		t.Fatalf("tests: %+v", cases)
	}

	for name, want := range map[string]string{
		"TestAdd":         "go:math:Add",
		"TestMeter_Count": "go:math:Meter.Count",
		"TestExternal":    "go:math:Sub",
	} {
		each := found[name]
		if each.Module != "go:math" || each.File == "" || each.Line == 0 || !slices.Contains(each.Targets, want) {
			t.Errorf("%s: %+v, want a target %s", name, each, want)
		}
	}

	if slices.Contains(found["TestAdd"].Targets, "go:math:Sub") {
		t.Errorf("TestAdd targets Sub: %v", found["TestAdd"].Targets)
	}
}

func TestRunReportsOutcomesAndCoverage(t *testing.T) {
	root := writeTree(t, mathTree)
	outcomes := map[string]app.TestOutcome{}
	coverage, err := NewTestSuite().Run(context.Background(), root, []app.TestRequest{{Module: "go:math", Names: []string{"TestAdd", "TestBroken", "TestMeter_Count/once"}}}, func(outcome app.TestOutcome) {
		outcomes[outcome.Name] = outcome
	})

	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"TestAdd": app.TestPassed, "TestBroken": app.TestFailed, "TestMeter_Count": app.TestPassed, "TestMeter_Count/once": app.TestPassed} {
		if outcomes[name].Status != want {
			t.Errorf("%s: %+v, want %s", name, outcomes[name], want)
		}
	}

	if outcomes["TestBroken"].Output == "" || outcomes["TestExternal"].Status != "" {
		t.Errorf("outcomes: %+v", outcomes)
	}

	lines := coverage["math/math.go"]
	if !slices.Contains(lines.Covered, 4) || !slices.Contains(lines.Uncovered, 9) || slices.Contains(lines.Covered, 9) {
		t.Fatalf("coverage: %+v", coverage)
	}
}

func TestRunReportsAPackageThatDoesNotBuild(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":            "module example.com/broken\n\ngo 1.22\n",
		"lib/lib.go":        "package lib\n\nfunc Lib() int { return missing }\n",
		"lib/lib_test.go":   "package lib\n\nimport \"testing\"\n\nfunc TestLib(t *testing.T) { Lib() }\n",
		"other/other.go":    "package other\n\nfunc Other() int { return 1 }\n",
		"other/one_test.go": "package other\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) { Other() }\n",
	})

	var modules []app.TestOutcome
	_, err := NewTestSuite().Run(context.Background(), root, []app.TestRequest{{Module: "go:lib"}, {Module: "go:other"}}, func(outcome app.TestOutcome) {
		if outcome.Name == "" {
			modules = append(modules, outcome)
		}
	})

	if err != nil {
		t.Fatal(err)
	}

	statuses := map[string]app.TestOutcome{}
	for _, each := range modules {
		statuses[each.Module] = each
	}

	if statuses["go:lib"].Status != app.TestFailed || statuses["go:lib"].Output == "" || statuses["go:other"].Status != app.TestPassed {
		t.Fatalf("modules: %+v", modules)
	}
}

func TestPlanGroupsWholeModulesAndQuotesNames(t *testing.T) {
	roots := moduleRoots{{path: "example.com/nested", dir: "nested"}, {path: "example.com/top", dir: "."}}
	got := plan(roots, []app.TestRequest{
		{Module: "go:a"},
		{Module: "go:."},
		{Module: "go:nested/b"},
		{Module: "go:c", Names: []string{"TestX", "TestY/with.dot"}},
	})

	want := []invocation{
		{dir: ".", packages: []string{"./a", "."}},
		{dir: "nested", packages: []string{"./b"}},
		{dir: ".", packages: []string{"./c"}, run: "^TestY$/^with\\.dot$"},
		{dir: ".", packages: []string{"./c"}, run: "^(TestX)$"},
	}

	if len(got) != len(want) {
		t.Fatalf("plan: %+v", got)
	}

	for index := range want {
		if got[index].dir != want[index].dir || got[index].run != want[index].run || !slices.Equal(got[index].packages, want[index].packages) {
			t.Errorf("invocation %d: %+v, want %+v", index, got[index], want[index])
		}
	}
}
