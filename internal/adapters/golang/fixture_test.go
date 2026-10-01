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
	report, err := newService(t, "../../..").Check("")
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
