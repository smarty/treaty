package golang

import (
	"errors"
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
	if err := os.WriteFile(filepath.Join(root, "treaty.yaml"), []byte("architecture: hexagonal\nlayers:\n  domain: [\"internal/**\"]\n  application: [\"injection/**\"]\n  adapter:\n    driven: [\"adapters/**\"]\n  composition: [\"cmd/**\"]\n"), 0o644); err != nil {
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

func TestExistingViolationsDoNotFail(t *testing.T) {
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

	// The base already has store → web, which breaks the layered rules.
	write(map[string]string{
		"go.mod":         "module example.com/m\n",
		"treaty.yaml":    "architecture: layered\nlayers:\n  web:   [\"web/**\"]\n  core:  [\"core/**\"]\n  store: [\"store/**\"]\n",
		"web/web.go":     "package web\n\nfunc Page() {}\n",
		"core/core.go":   "package core\n\nfunc Rule() {}\n",
		"store/store.go": "package store\n\nimport \"example.com/m/web\"\n\nfunc Save() {\n\tweb.Page()\n}\n",
	})
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "before")
	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(t.TempDir()), &viewRecorder{}, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))

	// Without a base every violation is new, so the existing one fails.
	report, err := service.Check("")
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Failures) != 1 || len(report.Violations) != 1 || report.Violations[0].Existing {
		t.Fatalf("without a base: failures %v, violations %+v", report.Failures, report.Violations)
	}

	// Against a base that has it, the violation is listed but passes.
	write(map[string]string{"store/store.go": "package store\n\nimport \"example.com/m/web\"\n\nfunc Save() {\n\tweb.Page()\n\tweb.Page()\n}\n"})
	if report, err = service.Check("HEAD"); err != nil {
		t.Fatal(err)
	}

	if len(report.Failures) != 0 || len(report.Violations) != 1 || !report.Violations[0].Existing {
		t.Fatalf("existing only: failures %v, violations %+v", report.Failures, report.Violations)
	}

	if !slices.ContainsFunc(report.Notes, func(note string) bool { return strings.Contains(note, "1 violation(s) were already in base HEAD") }) {
		t.Fatalf("notes: %v", report.Notes)
	}

	// A violation the change introduces fails, and ranks above the old one.
	write(map[string]string{"store/store.go": "package store\n\nimport (\n\t\"example.com/m/core\"\n\t\"example.com/m/web\"\n)\n\nfunc Save() {\n\tcore.Rule()\n\tweb.Page()\n}\n"})
	if report, err = service.Check("HEAD"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(report.Failures, []string{"1 layer violation(s)"}) || len(report.Violations) != 2 {
		t.Fatalf("new and existing: failures %v, violations %+v", report.Failures, report.Violations)
	}

	for _, violation := range report.Violations {
		if want := violation.To == "go:web"; violation.Existing != want {
			t.Fatalf("%s → %s: existing %v", violation.From, violation.To, violation.Existing)
		}
	}

	summary := report.Summary()
	if !strings.Contains(summary, "go:store → go:web (existing)") || strings.Contains(summary, "go:store → go:core (existing)") {
		t.Fatalf("summary:\n%s", summary)
	}

	var titles []string
	for _, finding := range report.Findings {
		if finding.Kind == rules.FindingLayerViolation {
			titles = append(titles, finding.Title)
		}
	}

	if !slices.Equal(titles, []string{"go:store → go:core breaks a layer rule", "go:store → go:web breaks a layer rule (existing)"}) {
		t.Fatalf("violation findings: %v", titles)
	}
}

func TestUnplacedModulesFailOrWarn(t *testing.T) {
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

	// docs matches no glob in the base already. legacy/orders is claimed
	// by orders and legacy equally.
	config := "version: 1\narchitecture: vertical\nboundaries:\n  orders: [\"**/orders/**\"]\n  legacy: [\"legacy/**\"]\nrules:\n  fail_on: [unclassified]\n  warn_on: [ambiguous_boundaries]\n"
	write(map[string]string{
		"go.mod":                  "module example.com/m\n",
		"treaty.yaml":             config,
		"web/orders/orders.go":    "package orders\n\nfunc Page() {}\n",
		"docs/docs.go":            "package docs\n\nfunc Read() {}\n",
		"legacy/orders/legacy.go": "package orders\n\nfunc Old() {}\n",
	})
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "before")
	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(t.TempDir()), &viewRecorder{}, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))

	report, err := service.Check("")
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(report.Failures, []string{"1 unclassified module(s)"}) || !slices.Equal(report.Warnings, []string{"1 module(s) claimed equally by two or more boundaries"}) {
		t.Fatalf("without a base: failures %v, warnings %v", report.Failures, report.Warnings)
	}

	if !slices.ContainsFunc(report.Findings, func(finding rules.Finding) bool {
		return finding.Kind == rules.FindingAmbiguous && finding.Title == "go:legacy/orders is claimed by boundaries orders and legacy"
	}) {
		t.Fatalf("findings: %+v", report.Findings)
	}

	// Against the base, docs was already unclassified, but tools is new.
	write(map[string]string{"tools/tools.go": "package tools\n\nfunc Run() {}\n"})
	if report, err = service.Check("HEAD"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(report.Failures, []string{"1 unclassified module(s)"}) || len(report.Warnings) != 0 {
		t.Fatalf("against the base: failures %v, warnings %v", report.Failures, report.Warnings)
	}

	if summary := report.Summary(); !strings.Contains(summary, "FAIL: 1 unclassified module(s)") {
		t.Fatalf("summary:\n%s", summary)
	}
}

// pipelineRecorder is a CI service that keeps what it is given.
type pipelineRecorder struct {
	target  string
	reviews []app.Review
}

func (this *pipelineRecorder) Name() string   { return "recorder" }
func (this *pipelineRecorder) Target() string { return this.target }

func (this *pipelineRecorder) Publish(review app.Review) error {
	this.reviews = append(this.reviews, review)
	return nil
}

func TestReviewComparesWithTheMergeBase(t *testing.T) {
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
		if output, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	write(map[string]string{
		"go.mod":      "module example.com/m\n",
		"treaty.yaml": "version: 1\narchitecture: layered\nlayers:\n  web: [\"web/**\"]\n  store: [\"store/**\"]\nrules:\n  fail_on: [breaking_changes, layer_violation]\n  warn_on: [unclassified]\n",
		"web/web.go":  "package web\n\nfunc Page() {}\n\nfunc Old() {}\n",
		"store/db.go": "package store\n\nfunc Open() {}\n",
		".gitignore":  ".treaty/\n",
	})
	git("init", "-q", "-b", "master")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	// The branch removes Old, makes store use web, and adds a module no
	// layer places.
	git("checkout", "-q", "-b", "feature")
	write(map[string]string{
		"web/web.go":     "package web\n\nfunc Page() {}\n",
		"store/db.go":    "package store\n\nimport \"example.com/m/web\"\n\nfunc Open() { web.Page() }\n",
		"tools/tools.go": "package tools\n\nfunc Run() {}\n",
	})
	git("add", ".")
	git("commit", "-q", "-m", "feature")

	// Master moves on after the branch left it: compared with master's tip,
	// the branch would seem to remove Later.
	git("checkout", "-q", "master")
	write(map[string]string{"web/later.go": "package web\n\nfunc Later() {}\n"})
	git("add", ".")
	git("commit", "-q", "-m", "later")
	git("checkout", "-q", "feature")

	recorder := &viewRecorder{}
	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(root), recorder, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	pipeline := &pipelineRecorder{target: "master"}
	service.UsePipeline(pipeline)

	review, err := service.Review("")
	if err != nil {
		t.Fatal(err)
	}

	if len(pipeline.reviews) != 1 || review.Target != "master" || review.Map != filepath.Join(".treaty", "out", "map.html") {
		t.Fatalf("published %d review(s), target %q, map %q", len(pipeline.reviews), review.Target, review.Map)
	}

	report := review.Report
	if !slices.Equal(report.Failures, []string{"1 breaking change(s)", "1 layer violation(s)"}) || !slices.Equal(report.Warnings, []string{"1 unclassified module(s)"}) {
		t.Fatalf("failures %v, warnings %v", report.Failures, report.Warnings)
	}

	type where struct {
		kind, verdict, file string
		line                int
	}

	var found []where
	for _, finding := range report.Findings {
		if finding.Verdict != "" {
			found = append(found, where{finding.Kind, finding.Verdict, finding.File, finding.Line})
		}
	}

	slices.SortFunc(found, func(a, b where) int { return strings.Compare(a.kind, b.kind) })
	expected := []where{
		{rules.FindingBreaking, rules.VerdictFail, "web/web.go", 0},
		{rules.FindingLayerViolation, rules.VerdictFail, "store/db.go", 5},
		{rules.FindingUnclassified, rules.VerdictWarn, "tools/tools.go", 0},
	}

	if !slices.Equal(found, expected) {
		t.Fatalf("judged findings:\n got %+v\nwant %+v", found, expected)
	}

	// The map's review queue says the same as the check.
	if !slices.Equal(recorder.view.Failures, report.Failures) || !slices.Equal(recorder.view.Warnings, report.Warnings) || !slices.ContainsFunc(recorder.view.Findings, func(finding rules.Finding) bool { return finding.Verdict == rules.VerdictWarn }) {
		t.Fatalf("map verdict: failures %v, warnings %v", recorder.view.Failures, recorder.view.Warnings)
	}

	if markdown := report.Markdown(); !strings.Contains(markdown, "### Treaty: ❌ fails") || !strings.Contains(markdown, "`store/db.go:5`") {
		t.Fatalf("markdown:\n%s", markdown)
	}

	if _, _, err := service.PullRequestBase("nowhere"); !errors.Is(err, app.ErrNoTarget) {
		t.Fatalf("a missing target: %v", err)
	}
}

func TestIgnoredFindings(t *testing.T) {
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
		if output, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	write(map[string]string{
		"go.mod":      "module example.com/m\n",
		"treaty.yaml": "version: 1\narchitecture: layered\nlayers:\n  web: [\"web/**\"]\n  store: [\"store/**\"]\nrules:\n  fail_on: [breaking_changes, layer_violation, unclassified]\n",
		"web/web.go":  "package web\n\nfunc Page() {}\n",
		"store/db.go": "package store\n\nfunc Open() {}\n",
		".gitignore":  ".treaty/\n",
	})
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	// The change breaks Page, makes store use web, and adds tools, which no
	// layer places.
	write(map[string]string{
		"web/web.go":     "package web\n\nfunc Page(title string) {}\n",
		"store/db.go":    "package store\n\nimport \"example.com/m/web\"\n\nfunc Open() { web.Page(\"\") }\n",
		"tools/tools.go": "package tools\n\nfunc Run() {}\n",
	})

	config := filesystem.NewConfig(root)
	service := app.NewService(root, config, NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(root), &viewRecorder{}, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	check := func() app.Report {
		t.Helper()
		report, err := service.Check("HEAD")
		if err != nil {
			t.Fatal(err)
		}

		return report
	}

	find := func(report app.Report, kind string) rules.Finding {
		t.Helper()
		for _, finding := range report.Findings {
			if finding.Kind == kind {
				return finding
			}
		}

		t.Fatalf("no %s finding in %+v", kind, report.Findings)
		return rules.Finding{}
	}

	report := check()
	if !slices.Equal(report.Failures, []string{"1 breaking change(s)", "1 layer violation(s)", "1 unclassified module(s)"}) {
		t.Fatalf("before ignoring: %v", report.Failures)
	}

	for _, kind := range []string{rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified} {
		finding := find(report, kind)
		if _, err := config.Ignore(app.Ignore{Finding: kind, Target: finding.Target, Fingerprint: finding.Fingerprint, Reason: "expected: " + kind}); err != nil {
			t.Fatal(err)
		}
	}

	report = check()
	if len(report.Failures) != 0 || find(report, rules.FindingLayerViolation).Ignored != "expected: layer_violation" || !strings.Contains(report.Summary(), "ignored: 3 finding(s)") {
		t.Fatalf("ignored: failures %v\n%s", report.Failures, report.Summary())
	}

	if data, _ := os.ReadFile(filepath.Join(root, "treaty.yaml")); !strings.Contains(string(data), "target: go:store -> go:web") || !strings.Contains(string(data), "reason: 'expected: layer_violation'") {
		t.Fatalf("treaty.yaml:\n%s", data)
	}

	// Breaking Page another way changes the finding: it counts again,
	// marked as once ignored.
	write(map[string]string{"web/web.go": "package web\n\nfunc Page(title string, size int) {}\n", "store/db.go": "package store\n\nimport \"example.com/m/web\"\n\nfunc Open() { web.Page(\"\", 0) }\n"})
	report = check()
	if breaking := find(report, rules.FindingBreaking); breaking.Ignored != "" || breaking.WasIgnored != "expected: breaking" || breaking.Verdict != rules.VerdictFail || !slices.Equal(report.Failures, []string{"1 breaking change(s)"}) {
		t.Fatalf("changed since ignored: %+v, failures %v", breaking, report.Failures)
	}

	// An entry whose target is gone is stale.
	if err := os.RemoveAll(filepath.Join(root, "tools")); err != nil {
		t.Fatal(err)
	}

	if report = check(); !slices.ContainsFunc(report.Notes, func(note string) bool {
		return strings.Contains(note, "ignores unclassified go:tools at line") && strings.Contains(note, "no longer in the code")
	}) {
		t.Fatalf("notes: %v", report.Notes)
	}

	if _, err := config.Unignore(rules.FindingUnclassified, "go:tools"); err != nil {
		t.Fatal(err)
	}

	if report = check(); slices.ContainsFunc(report.Notes, func(note string) bool { return strings.Contains(note, "go:tools") }) {
		t.Fatalf("unignored, the note must go: %v", report.Notes)
	}
}

func TestIgnoredSidewaysDependency(t *testing.T) {
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
		if output, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}

	write(map[string]string{
		"go.mod":                  "module example.com/m\n",
		"treaty.yaml":             "version: 1\narchitecture: snowflake\ncomposition: [\"cmd/**\"]\nstars:\n  internal: [\"internal/core/**\"]\nrules:\n  fail_on: [breaking_changes, layer_violation, unclassified]\n",
		"cmd/app/main.go":         "package main\n\nimport (\n\t\"example.com/m/internal/plan\"\n\t\"example.com/m/internal/web\"\n)\n\nfunc main() { plan.Run(web.Fetcher{}) }\n",
		"internal/core/core.go":   "package core\n\n// Fetcher fetches a page.\ntype Fetcher interface{ Fetch() string }\n",
		"internal/plan/plan.go":   "package plan\n\nimport \"example.com/m/internal/core\"\n\n// Run runs a plan.\nfunc Run(fetcher core.Fetcher) { fetcher.Fetch() }\n",
		"internal/web/web.go":     "package web\n\n// Fetcher fetches pages.\ntype Fetcher struct{}\n\n// Fetch fetches a page.\nfunc (Fetcher) Fetch() string { return \"\" }\n",
		"internal/web/helpers.go": "package web\n\n// Title names a page.\nfunc Title() string { return \"\" }\n",
		".gitignore":              ".treaty/\n",
	})
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	config := filesystem.NewConfig(root)
	service := app.NewService(root, config, NewExtractor(), []app.Dialect{NewDialect()},
		gitvcs.New(root), filesystem.NewWorkspace(root), &viewRecorder{}, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	check := func() app.Report {
		t.Helper()
		report, err := service.Check("HEAD")
		if err != nil {
			t.Fatal(err)
		}

		return report
	}

	violation := func(report app.Report) rules.Finding {
		t.Helper()
		for _, finding := range report.Findings {
			if finding.Kind == rules.FindingLayerViolation {
				return finding
			}
		}

		t.Fatalf("no layer violation in %+v", report.Findings)
		return rules.Finding{}
	}

	if report := check(); len(report.Failures) != 0 {
		t.Fatalf("a clean snowflake: %v", report.Failures)
	}

	// The plan spoke calls its sibling web directly.
	write(map[string]string{"internal/plan/plan.go": "package plan\n\nimport (\n\t\"example.com/m/internal/core\"\n\t\"example.com/m/internal/web\"\n)\n\n// Run runs a plan.\nfunc Run(fetcher core.Fetcher) { fetcher.Fetch(); web.Title() }\n"})
	report := check()
	finding := violation(report)
	if !slices.Equal(report.Failures, []string{"1 layer violation(s)"}) || finding.Target != "go:internal/plan -> go:internal/web" ||
		!strings.Contains(finding.Detail, "spoke internal/plan may not depend on spoke internal/web: internal/plan and internal/web are sibling spokes of star internal") {
		t.Fatalf("sideways: failures %v, %+v", report.Failures, finding)
	}

	// It is declared like any other boundary violation.
	if _, err := config.Ignore(app.Ignore{Finding: finding.Kind, Target: finding.Target, Fingerprint: finding.Fingerprint, Reason: "plan moves behind a port next"}); err != nil {
		t.Fatal(err)
	}

	if report = check(); len(report.Failures) != 0 || len(report.Warnings) != 0 || violation(report).Ignored != "plan moves behind a port next" {
		t.Fatalf("ignored: failures %v, warnings %v", report.Failures, report.Warnings)
	}

	// Turning the dependency around is another edge: it counts, and the
	// entry for the edge that is gone is stale.
	write(map[string]string{
		"internal/plan/plan.go":   "package plan\n\nimport \"example.com/m/internal/core\"\n\n// Run runs a plan.\nfunc Run(fetcher core.Fetcher) { fetcher.Fetch() }\n\n// Name names a plan.\nfunc Name() string { return \"\" }\n",
		"internal/web/helpers.go": "package web\n\nimport \"example.com/m/internal/plan\"\n\n// Title names a page.\nfunc Title() string { return plan.Name() }\n",
	})
	report = check()
	if finding = violation(report); !slices.Equal(report.Failures, []string{"1 layer violation(s)"}) || finding.Target != "go:internal/web -> go:internal/plan" || finding.Ignored != "" {
		t.Fatalf("a new edge: failures %v, %+v", report.Failures, finding)
	}

	if !slices.ContainsFunc(report.Notes, func(note string) bool {
		return strings.Contains(note, "ignores layer_violation go:internal/plan -> go:internal/web at line") && strings.Contains(note, "no longer in the code")
	}) {
		t.Fatalf("notes: %v", report.Notes)
	}

	// Once the base has it, it is existing debt and does not fail.
	git("add", ".")
	git("commit", "-q", "-m", "sideways")
	if report = check(); len(report.Failures) != 0 || !violation(report).Existing {
		t.Fatalf("existing: failures %v, %+v", report.Failures, violation(report))
	}
}

func TestSnowflakeFromInitToTheTools(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":                             "module example.com/m\n",
		"m.go":                               "package m\n\nimport \"example.com/m/internal/factory\"\n\n// Run runs the guard.\nfunc Run() { factory.New() }\n",
		"cmd/app/main.go":                    "package main\n\nimport \"example.com/m\"\n\nfunc main() { m.Run() }\n",
		"internal/factory/factory.go":        "package factory\n\nimport (\n\t\"example.com/m/internal/blocks\"\n\t\"example.com/m/internal/blocks/cleanse\"\n\t\"example.com/m/internal/blocks/www\"\n\t\"example.com/m/internal/plan\"\n\t\"example.com/m/internal/web\"\n)\n\n// New wires the guard.\nfunc New() { plan.Run(web.Client{}); blocks.Run(www.Step{}, cleanse.Step{}) }\n",
		"internal/core/core.go":              "package core\n\n// Fetcher fetches a page.\ntype Fetcher interface{ Fetch() string }\n",
		"internal/plan/plan.go":              "package plan\n\nimport \"example.com/m/internal/core\"\n\n// Run runs a plan.\nfunc Run(fetcher core.Fetcher) { fetcher.Fetch() }\n",
		"internal/web/web.go":                "package web\n\nimport \"example.com/m/internal/core\"\n\n// Client fetches pages.\ntype Client struct{}\n\n// Fetch fetches a page.\nfunc (Client) Fetch() string { return \"\" }\n\nvar _ core.Fetcher = Client{}\n",
		"internal/blocks/blocks.go":          "package blocks\n\nimport \"example.com/m/internal/blocks/core\"\n\n// Run runs the steps.\nfunc Run(steps ...core.Step) {\n\tfor _, step := range steps {\n\t\tstep.Do()\n\t}\n}\n",
		"internal/blocks/core/core.go":       "package core\n\nimport \"example.com/m/internal/core\"\n\n// Step is one block.\ntype Step interface{ Do() }\n\n// Source is where a step reads from.\ntype Source = core.Fetcher\n",
		"internal/blocks/www/www.go":         "package www\n\nimport \"example.com/m/internal/blocks/core\"\n\n// Step reads the web.\ntype Step struct{}\n\n// Do reads.\nfunc (Step) Do() {}\n\nvar _ core.Step = Step{}\n",
		"internal/blocks/cleanse/cleanse.go": "package cleanse\n\nimport \"example.com/m/internal/blocks/core\"\n\n// Step cleans text.\ntype Step struct{}\n\n// Do cleans.\nfunc (Step) Do() {}\n\nvar _ core.Step = Step{}\n",
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	recorder := &viewRecorder{}
	config := filesystem.NewConfig(root)
	service := app.NewService(root, config, NewExtractor(), []app.Dialect{NewDialect()},
		nil, filesystem.NewWorkspace(root), recorder, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))

	// init drafts the stars, and the draft loads and passes.
	if _, err := service.Init(rules.StyleSnowflake); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []rules.Star{{Dir: "internal", Core: []string{"internal/core/**"}}, {Dir: "internal/blocks", Core: []string{"internal/blocks/core/**"}}}
	if architecture := loaded.Architecture; !slices.Equal(architecture.Composition, []string{".", "cmd/**", "internal/factory/**"}) || len(architecture.Stars) != 2 ||
		architecture.Stars[0].Dir != want[0].Dir || !slices.Equal(architecture.Stars[0].Core, want[0].Core) || architecture.Stars[1].Dir != want[1].Dir || !slices.Equal(architecture.Stars[1].Core, want[1].Core) {
		t.Fatalf("init drafted %+v", architecture)
	}

	report, err := service.Check("")
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Failures) > 0 || len(report.Violations) > 0 {
		t.Fatalf("the draft passes: %v %+v", report.Failures, report.Violations)
	}

	// Every copy of a placement carries the star and the spoke.
	for _, module := range report.Modules {
		got := rules.Placement{Layer: module.Layer, Side: module.Side, Boundary: module.Boundary, Public: module.Public, Star: module.Star, Spoke: module.Spoke}
		if want := loaded.Architecture.Resolve(strings.TrimPrefix(module.ID, "go:")); !got.Same(want) {
			t.Errorf("the report places %s in %+v, not %+v", module.ID, got, want)
		}
	}

	if _, err := service.Map("", nil); err != nil {
		t.Fatal(err)
	}

	if view := recorder.view; !slices.Equal(view.Layers, []string{"core", "spoke"}) || len(view.Stars) != 2 || view.Stars[1].Parent != "internal" {
		t.Fatalf("the map's layers %v and stars %+v", view.Layers, view.Stars)
	}

	for _, module := range recorder.view.Modules {
		got := rules.Placement{Layer: module.Layer, Side: module.Side, Boundary: module.Boundary, Public: module.Public, Ambiguous: module.Ambiguous, Star: module.Star, Spoke: module.Spoke}
		if want := loaded.Architecture.Resolve(module.Path); !got.Same(want) {
			t.Errorf("the map places %s in %+v, not %+v", module.ID, got, want)
		}
	}

	// allowed gives the rule, and the ports that already stand for what
	// the spoke reached for, or says to declare one.
	for _, c := range []struct{ from, to, want string }{
		{"internal/plan", "internal/web", "not allowed: go:internal/plan (spoke internal/plan) → go:internal/web (spoke internal/web)\n" +
			"rule: spoke internal/plan may not depend on spoke internal/web: internal/plan and internal/web are sibling spokes of star internal, and spokes never depend sideways; declare a port in the core of star internal, implement it in internal/web, and let composition hand it in\n" +
			"ports: go:internal/web:Client implements go:internal/core:Fetcher, which spoke internal/plan may use; depend on the port, and let composition hand internal/web in\n" +
			"spoke internal/plan may depend on: spoke internal/plan, core of internal (internal, internal/core/**)\n"},
		{"internal/plan", "internal/blocks/www", "ports: nothing in internal/blocks implements an interface in a core spoke internal/plan may use; declare one in the core of star internal\n"},
		{"internal/blocks/www", "internal/core", "allowed: go:internal/blocks/www (spoke internal/blocks/www) → go:internal/core (core of star internal)\n"},
		{"internal/plan", "internal/blocks/new", "go:internal/plan (spoke internal/plan) → internal/blocks/new (not built) (spoke internal/blocks/new)\n"},
	} {
		got, err := service.Allowed(c.from, c.to)
		if err != nil || !strings.Contains(got, c.want) {
			t.Errorf("allowed %s %s = %q, %v; want %q", c.from, c.to, got, err, c.want)
		}
	}

	overview, err := service.Overview("")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"Rules (snowflake): the code is a star of stars.",
		"\nstars\n  internal  core internal, internal/core/**  leaves internal/plan, internal/web  branches internal/blocks\n  internal/blocks (branch of internal)  core internal/blocks, internal/blocks/core/**  leaves internal/blocks/cleanse, internal/blocks/www\n",
		"\ncomposition\n",
		"\ncore of star internal\n  go:internal/core",
		"\nspoke internal/blocks/www\n  go:internal/blocks/www",
	} {
		if !strings.Contains(overview, want) {
			t.Errorf("the overview lacks %q:\n%s", want, overview)
		}
	}

	if strings.Index(overview, "\ncore of star internal\n") > strings.Index(overview, "\nspoke internal/plan\n") || strings.Index(overview, "\nspoke internal/web\n") > strings.Index(overview, "\ncore of star internal/blocks\n") {
		t.Errorf("each star lists its core, then its leaves, before the stars inside it:\n%s", overview)
	}

	slice, err := service.Slice("go:internal/blocks/www")
	if err != nil {
		t.Fatal(err)
	}

	if star := slice.Star; star == nil || star.Dir != "internal/blocks" || star.Parent != "internal" || star.Role != "spoke" || star.Spoke != "internal/blocks/www" ||
		!slices.Equal(star.Cores, []string{"go:internal/blocks", "go:internal/blocks/core", "go:internal/core"}) || !slices.Equal(star.Siblings, []string{"internal/blocks/cleanse"}) {
		t.Fatalf("the slice's star: %+v", slice.Star)
	}

	if text := slice.Text(); !strings.Contains(text, "star: spoke internal/blocks/www of star internal/blocks, a branch of internal\n  cores it may use: go:internal/blocks, go:internal/blocks/core, go:internal/core\n  never use: internal/blocks/cleanse\n") {
		t.Fatalf("the slice's text:\n%s", text)
	}

	if slice, err = service.Slice("go:internal/core"); err != nil || slice.Star == nil || !slices.Equal(slice.Star.OwnSpokes, []string{"internal/blocks (branch)", "internal/plan", "internal/web"}) {
		t.Fatalf("a core's own spokes: %+v %v", slice.Star, err)
	}
}
