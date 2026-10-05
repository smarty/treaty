package golang

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/adapters/gitvcs"
	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

// The fixture reproduces the mockup's smarty/injection PR #142:
// testdata/injection-base is the tree before it and testdata/injection the
// tree after it.

var update = flag.Bool("update", false, "rewrite the golden files")

func TestOwnRepositoryPasses(t *testing.T) {
	report, err := newService(t, "../../../..").Check("")
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Failures) > 0 || len(report.Violations) > 0 {
		t.Fatalf("treaty check must pass on treaty itself: %v %+v", report.Failures, report.Violations)
	}
}

func TestFixtureSlice(t *testing.T) {
	slice, err := newService(t, "testdata/injection").Slice("go:injection:Container.Resolve")
	if err != nil {
		t.Fatal(err)
	}

	text := slice.Text()
	for _, want := range []string{
		"slice go:injection:Container.Resolve  method  injection/container.go:15-15  layer application",
		"contract: func Resolve(ctx context.Context, k Key) (any, error)",
		"may depend on: domain, application",
		"neighbors (1):\n  uses       Key  injection/container.go:9  type Key struct",
		"excluded: 5 modules, 19 symbols",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the slice must contain %q:\n%s", want, text)
		}
	}
}

func TestAllowedTakesShortNames(t *testing.T) {
	service := newService(t, "testdata/injection")
	for _, c := range []struct{ from, to, want string }{
		{"graph", "reflectx", "not allowed: go:internal/graph (domain) → go:adapters/reflectx (adapter (driven))"},
		{"internal/graph", "go:adapters/reflectx", "not allowed: go:internal/graph (domain)"},
		{"Graph.Order", "names.go", "allowed: go:internal/graph (domain) → go:internal/graph (domain)"},
		{"injection", "internal/planned", "allowed: go:injection (application) → internal/planned (not built) (domain)"},
	} {
		got, err := service.Allowed(c.from, c.to)
		if err != nil || !strings.HasPrefix(got, c.want) {
			t.Errorf("allowed %s %s = %q, %v; want %q", c.from, c.to, got, err, c.want)
		}
	}
}

func TestFindFields(t *testing.T) {
	service := newService(t, "testdata/injection")
	got, err := service.Find("ttl", "")
	if err != nil || got != "go:internal/graph:Lifetime.TTL  field  internal/graph/graph.go:5  TTL int\n" {
		t.Fatalf("find must match a field by name: %q %v", got, err)
	}

	if got, _ := service.Find("Key.kind", "field"); !strings.HasPrefix(got, "go:injection:Key.kind  field") {
		t.Fatalf("find --kind field lists unexported fields too: %q", got)
	}

	if got, _ := service.Find("TTL", "type"); got != "no symbol matches\n" {
		t.Fatalf("another kind leaves fields out: %q", got)
	}
}

func TestSourceOutlinesLongFiles(t *testing.T) {
	root := t.TempDir()
	copyTree(t, "testdata/injection", root)
	var long strings.Builder
	long.WriteString("package sorting\n\n// Long has many lines.\nfunc Long() int {\n\ttotal := 0\n")
	for i := range 150 {
		fmt.Fprintf(&long, "\ttotal += %d\n", i)
	}

	long.WriteString("\treturn total\n}\n\nfunc Short() {}\n")
	if err := os.WriteFile(filepath.Join(root, "internal/sorting/long.go"), []byte(long.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	service := newService(t, root)
	outline, err := service.Source("long.go", false)
	if err != nil || !strings.Contains(outline, "more than source prints at once (120)") || !strings.Contains(outline, "Long") || strings.Contains(outline, "total += 5\n") {
		t.Fatalf("a long file prints its outline: %v\n%s", err, outline)
	}

	for _, target := range []string{"long.go:1-40", "Long"} {
		if text, err := service.Source(target, false); err != nil || !strings.Contains(text, "total +=") {
			t.Errorf("%s prints its lines: %v\n%.200s", target, err, text)
		}
	}

	if text, _ := service.Source("long.go", true); !strings.Contains(text, "total += 149") {
		t.Fatal("all prints the whole file")
	}
}

func TestUnclassifiedOnlyWarns(t *testing.T) {
	root := t.TempDir()
	copyTree(t, "testdata/injection", root)
	if err := os.WriteFile(filepath.Join(root, "treaty.yaml"), []byte("layers:\n  domain: [\"internal/**\"]\n  application: [\"injection/**\"]\n  adapter:\n    driven: [\"adapters/**\"]\n  composition: [\"cmd/**\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "tools", "tools.go"), []byte("package tools\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := newService(t, root).Check("")
	if err != nil {
		t.Fatal(err)
	}

	unclassified := slices.ContainsFunc(report.Findings, func(finding rules.Finding) bool {
		return finding.Kind == rules.FindingUnclassified && slices.Contains(finding.Targets, "go:tools")
	})

	if !unclassified || slices.ContainsFunc(report.Failures, func(failure string) bool { return strings.Contains(failure, "unclassified") }) {
		t.Fatalf("an unclassified module is a warning, never a failure: %v %+v", report.Failures, report.Findings)
	}
}

func TestFixtureDiff(t *testing.T) {
	report := fixtureDiff(t)
	kinds := map[string]string{}
	var implementation []string
	for _, change := range report.Changes {
		kinds[change.Symbol] = change.Kind
		if change.Kind == rules.ChangeImplementation {
			implementation = append(implementation, change.Symbol)
		}
	}

	for symbol, want := range map[string]string{
		"go:injection:Container":         rules.ChangeBreaking,
		"go:injection:Container.Resolve": rules.ChangeBreaking,
		"go:internal/graph:Lifetime":     rules.ChangeContract,
		"go:internal/graph:Binding":      rules.ChangeContract,
	} {
		if kinds[symbol] != want {
			t.Errorf("%s is %q, want %q", symbol, kinds[symbol], want)
		}
	}

	slices.Sort(implementation)
	if want := []string{"go:injection:container.Resolve", "go:injection:resolveLocked", "go:internal/graph:Graph.Order", "go:internal/graph:topoSort"}; !slices.Equal(implementation, want) {
		t.Errorf("implementation-only changes are %v, want %v", implementation, want)
	}

	for _, module := range report.Modules {
		if module.ID == "go:internal/graph" && (module.Before == nil || module.Before.Instability != 0.33 || module.Metrics.Instability != 0.4) {
			t.Errorf("graph's instability must go from 0.33 to 0.40: %+v %+v", module.Before, module.Metrics)
		}
	}
}

func TestFixtureReviewQueue(t *testing.T) {
	report := fixtureDiff(t)
	var lines []string
	for _, finding := range report.Findings {
		lines = append(lines, fmt.Sprintf("%-6s %-22s %s", finding.Severity, finding.Kind, finding.Title))
	}

	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "injection-queue.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run go test -run TestFixtureReviewQueue -update to create it", err)
	}

	if got != string(want) {
		t.Fatalf("the review queue changed; run go test -run TestFixtureReviewQueue -update if that is intended.\ngot:\n%swant:\n%s", got, want)
	}
}

// fixtureDiff checks the fixture's tree after the PR against a commit of the
// tree before it.
func fixtureDiff(t *testing.T) app.Report {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "testdata/injection-base", root)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "before"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.Name() != ".git" {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}

	copyTree(t, "testdata/injection", root)
	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	report, err := service.Check("HEAD")
	if err != nil {
		t.Fatal(err)
	}

	return report
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, _ := filepath.Rel(from, current)
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})

	if err != nil {
		t.Fatal(err)
	}
}

// viewRecorder is a map renderer that keeps the last view it was given.
type viewRecorder struct{ view app.MapView }

func (this *viewRecorder) Page() []byte { return nil }

func (this *viewRecorder) Payload(view app.MapView) ([]byte, error) {
	this.view = view
	return nil, nil
}

func (this *viewRecorder) Render(view app.MapView) ([]byte, error) {
	this.view = view
	return nil, nil
}

func TestMapShowsCodeChangesAndRemovals(t *testing.T) {
	root := t.TempDir()
	write := func(files map[string]string) {
		for name, text := range files {
			file := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	git := func(args ...string) {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	write(map[string]string{
		"go.mod":      "module example.com/m\n",
		"a/a.go":      "package a\n\nimport (\n\t\"example.com/m/b\"\n\t\"example.com/m/c\"\n\t\"example.com/m/d\"\n\t\"example.com/m/e\"\n)\n\nfunc Do() int {\n\te.Same()\n\treturn d.Work()\n}\n\nfunc Run() int {\n\treturn b.Help()\n}\n\nfunc Use() {\n\tc.One()\n}\n",
		"d/d.go":      "package d\n\nfunc Work() int {\n\treturn 1\n}\n",
		"e/e.go":      "package e\n\nfunc Same() {}\n",
		"c/c.go":      "package c\n\nfunc One() {}\n\nfunc Two() {}\n",
		"b/b.go":      "package b\n\nfunc Help() int {\n\treturn 1\n}\n",
		"b/extra.go":  "package b\n\nfunc Extra() {}\n",
		"gone/old.go": "package gone\n\nfunc Old() {}\n",
	})
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "before")
	for _, name := range []string{"b/extra.go", "gone/old.go"} {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}

	write(map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"example.com/m/c\"\n\t\"example.com/m/d\"\n\t\"example.com/m/e\"\n)\n\nfunc Do() int {\n\te.Same()\n\treturn d.Work()\n}\n\nfunc Run() int {\n\treturn 2\n}\n\nfunc Use() {\n\tc.One()\n\tc.Two()\n}\n",
		"d/d.go": "package d\n\nfunc Work(values ...int) int {\n\treturn len(values)\n}\n",
		"e/e.go": "package e\n\nfunc Same() {\n\tprintln()\n}\n",
		"b/b.go": "package b\n\nfunc Help() int {\n\tvalue := 1\n\treturn value\n}\n",
	})

	recorder := &viewRecorder{}
	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(t.TempDir()), recorder, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	if _, err := service.Map("HEAD", nil); err != nil {
		t.Fatal(err)
	}

	view := recorder.view
	symbols := map[string]app.MapSymbol{}
	for _, symbol := range view.Symbols {
		symbols[symbol.ID] = symbol
	}

	diffText := func(lines []app.DiffLine) string {
		var parts []string
		for _, line := range lines {
			parts = append(parts, line.Op+line.Text)
		}

		return strings.Join(parts, "\n")
	}

	help := symbols["go:b:Help"]
	if want := " func Help() int {\n-\treturn 1\n+\tvalue := 1\n+\treturn value\n }"; help.Change != rules.ChangeImplementation || diffText(help.Diff) != want {
		t.Errorf("Help's code change: %s %q", help.Change, diffText(help.Diff))
	}

	for _, id := range []string{"go:b:Extra", "go:gone:Old"} {
		if symbol := symbols[id]; !symbol.Removed || symbol.Change != rules.ChangeRemoved || len(symbol.Diff) == 0 || symbol.Diff[0].Op != app.DiffRemoved {
			t.Errorf("%s must show as removed with its old code: %+v", id, symbol)
		}
	}

	var removedModule bool
	for _, module := range view.Modules {
		if module.ID == "go:b" && !slices.Equal(module.RemovedFiles, []string{"b/extra.go"}) {
			t.Errorf("b's removed files: %q", module.RemovedFiles)
		}

		removedModule = removedModule || module.ID == "go:gone" && module.Removed
	}

	if !removedModule {
		t.Error("the deleted package must show as a removed module")
	}

	if work, same := symbols["go:d:Work"], symbols["go:e:Same"]; work.Change != rules.ChangeContract && work.Change != rules.ChangeBreaking || same.Change != rules.ChangeImplementation {
		t.Fatalf("the fixture needs Work's signature and only Same's code to change: %s, %s", work.Change, same.Change)
	}

	var removedEdge, changedEdge, contractEdge, implementationEdge bool
	for _, edge := range view.Edges {
		removedEdge = removedEdge || edge.From == "go:a" && edge.To == "go:b" && edge.Removed
		changedEdge = changedEdge || edge.From == "go:a" && edge.To == "go:c" && edge.Changed && !edge.New
		contractEdge = contractEdge || edge.From == "go:a" && edge.To == "go:d" && edge.Changed && !edge.New
		implementationEdge = implementationEdge || edge.From == "go:a" && edge.To == "go:e" && !edge.Changed && !edge.New
	}

	if !contractEdge {
		t.Error("the signature a's reference to d relies on changed, so a's dependency on d must show as changed")
	}

	if !implementationEdge {
		t.Error("only e's code changed, not its signature, so a's dependency on e must not show as changed")
	}

	if !removedEdge {
		t.Error("a's dropped dependency on b must show as a removed edge")
	}

	if !changedEdge {
		t.Error("a's dependency on c gained a reference, so it must show as changed")
	}

	if got := diffText(view.FileDiffs["b/b.go"]); got != " package b\n \n func Help() int {\n-\treturn 1\n+\tvalue := 1\n+\treturn value\n }" {
		t.Errorf("b.go's file diff: %q", got)
	}

	if got := diffText(view.FileDiffs["b/extra.go"]); got != "-package b\n-\n-func Extra() {}" {
		t.Errorf("a removed file is all removed: %q", got)
	}

	if _, ok := view.FileDiffs["go.mod"]; ok {
		t.Error("an unchanged file has no diff")
	}

	if len(view.RemovedLinks) != 1 || view.RemovedLinks[0].From != "go:a:Run" || view.RemovedLinks[0].To != "go:b:Help" {
		t.Errorf("removed references: %+v", view.RemovedLinks)
	}
}
