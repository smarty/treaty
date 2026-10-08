package golang

import (
	"context"
	"testing"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

func TestRunMeasuresReachDecisionsAndBoundaries(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"text/text.go": `package text

const MaxLength = 4

// Truncate cuts a value to MaxLength.
func Truncate(value string) string {
	if len(value) > MaxLength {
		return value[:MaxLength]
	}

	return value
}

// Kind names a value.
func Kind(value string, loud bool) string {
	if value == "" || loud {
		return "special"
	}

	return "plain"
}

func helper() int { return 1 }
`,
		"text/text_test.go": `package text

import "testing"

func TestTruncate(t *testing.T) {
	for _, value := range []string{"abc", "abcd", "abcde"} {
		if len(Truncate(value)) > MaxLength {
			t.Fatal(value)
		}
	}
}

func TestKind(t *testing.T) {
	if Kind("", false) != "special" {
		t.Fatal("kind")
	}
}

func TestParallelA(t *testing.T) {
	t.Parallel()
	_ = helper()
}

func TestParallelB(t *testing.T) {
	t.Parallel()
	_ = helper()
}
`,
	})

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	cases, err := NewTestSuite().Discover(root, g)
	if err != nil {
		t.Fatal(err)
	}

	var tests []rules.TestUse
	for _, each := range cases {
		tests = append(tests, rules.TestUse{ID: each.Module + "#" + each.Name, Kind: each.Kind, Targets: each.Targets})
	}

	coverage, err := NewTestSuite().Run(context.Background(), root, []app.TestRequest{{Module: "go:text"}}, func(app.TestOutcome) {})
	if err != nil {
		t.Fatal(err)
	}

	if !coverage.Shared["go:text#TestParallelA"] || !coverage.Shared["go:text#TestParallelB"] || coverage.Shared["go:text#TestTruncate"] {
		t.Errorf("only tests that ran alongside others are shared: %v", coverage.Shared)
	}

	modules, contracts := rules.Exercise(g, tests, coverage.Probes, coverage.Hits, coverage.Shared)
	check := func(what string, got rules.Exercised, want [6]int) {
		t.Helper()
		if values := [6]int{got.Blocks, got.BlocksRun, got.Outcomes, got.OutcomesRun, got.Boundaries, got.BoundariesSeen}; values != want {
			t.Errorf("%s: blocks, run, outcomes, run, boundaries, seen = %v, want %v", what, values, want)
		}
	}

	check("Truncate saw 3, 4 and 5 against MaxLength 4", contracts["go:text:Truncate"], [6]int{3, 3, 2, 2, 1, 1})
	check("Kind never saw the condition false, and short-circuiting skipped loud", contracts["go:text:Kind"], [6]int{3, 2, 6, 2, 0, 0})
	check("the module counts only tests that use its contracts", modules["go:text"], [6]int{7, 5, 8, 4, 1, 1})

	code := rules.UnderTest(g, tests, []string{"go:text#TestKind"}, coverage.Probes, coverage.Hits)
	if len(code.Files) != 1 || code.Files[0].File != "text/text.go" || len(code.Ran) != 1 {
		t.Fatalf("TestKind ran one file: %+v", code)
	}

	file := code.Files[0]
	if len(file.Ranges) != 1 || file.Ranges[0] != [2]int{15, 21} {
		t.Errorf("only Kind is shown, the code TestKind reached and uses: %v", file.Ranges)
	}

	lines := map[int]rules.LineUnderTest{}
	for _, line := range file.Lines {
		lines[line.Line] = line
	}

	condition := lines[16]
	if len(condition.Probes) != 7 || condition.Reached != 3 {
		t.Errorf("the condition's line holds its block, both outcomes and both operands' outcomes, and TestKind reached the block, true and the first operand's true: %d of %d", condition.Reached, len(condition.Probes))
	}

	for _, probe := range condition.Probes {
		if probe.Kind != rules.ProbeBlock && probe.Column == 0 {
			t.Errorf("a condition's probes span their expression: %+v", probe)
		}

		if probe.Operand && probe.Kind == rules.ProbeTrue && probe.Column == 5 && (probe.EndColumn != 16 || len(probe.Tests) != 1) {
			t.Errorf("value == \"\" spans columns 5 to 16 and TestKind reached its true: %+v", probe)
		}
	}

	if lines[17].Reached != 1 || lines[20].Reached != 0 || len(lines[20].Probes) != 1 {
		t.Errorf("the special return ran and the plain one did not: %+v, %+v", lines[17], lines[20])
	}
}
