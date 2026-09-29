package golang

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
)

const tricky = "package tricky\n" + `

import (
	"context"
	alias "strings"
)

// Doc comment with func Fake() inside.
const (
	A Kind = iota
	B
	c, D = 1, "two"
	Long = "` + "0123456789012345678901234567890123456789012345678901234567890123456789" + `"
)

var _ = alias.ToUpper

type Kind int

type (
	Pair[K comparable, V any] struct {
		Key   K
		Value V ` + "`json:\"value\"`" + `
		*Embedded
		context.Context
	}
	Array [4]int
	Alias = map[string][]int
)

type Doer interface {
	// Do does.
	Do(ctx context.Context, items ...string) (n int, err error)
	fmt() string
}

type Embedded struct{}

func (p *Pair[K, V]) Swap() Pair[K, V] { return *p }

func Generic[T any](values []T,
	keep func(T) bool,
) []T {
	s := ` + "`raw { string with } braces`" + `
	_ = s
	return nil
}

func (Embedded) fmt() string { return "}" }
`

func TestParseTricky(t *testing.T) {
	file := parseFile([]byte(tricky))
	if file.pkg != "tricky" || len(file.imports) != 2 || file.imports[1].alias != "alias" || file.imports[1].path != "strings" {
		t.Fatalf("package/imports: %q %+v", file.pkg, file.imports)
	}

	byName := map[string]*declaration{}
	for _, declaration := range file.declarations {
		byName[declaration.name] = declaration
	}

	for name, want := range map[string]string{
		"A":            "const A Kind = iota",
		"B":            "const B",
		"c":            "const c = 1",
		"D":            `const D = "two"`,
		"Long":         "const Long = …",
		"Kind":         "type Kind int",
		"Pair":         "type Pair[K comparable, V any] struct",
		"Array":        "type Array [4]int",
		"Alias":        "type Alias = map[string][]int",
		"Doer":         "type Doer interface",
		"Doer.Do":      "func Do(ctx context.Context, items ...string) (n int, err error)",
		"Doer.fmt":     "func fmt() string",
		"Pair.Swap":    "func Swap() Pair[K, V]",
		"Generic":      "func Generic[T any](values []T, keep func(T) bool) []T",
		"Embedded.fmt": "func fmt() string",
		"Embedded":     "type Embedded struct",
	} {
		got := byName[name]
		if got == nil {
			t.Errorf("%s: missing", name)
			continue
		}

		if got.signature != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got.signature, want)
		}
	}

	if len(file.declarations) != 16 {
		t.Errorf("want 16 declarations, got %d", len(file.declarations))
	}

	pair := byName["Pair"]
	if len(pair.fields) != 4 || pair.fields[1].Text != "Value V `json:\"value\"`" || len(pair.embedded) != 2 {
		t.Errorf("Pair fields %+v embedded %d", pair.fields, len(pair.embedded))
	}

	if swap := byName["Pair.Swap"]; !swap.pointer || swap.parent != "Pair" || swap.receiver != "p" {
		t.Errorf("Swap receiver: %+v", swap)
	}

	if generic := byName["Generic"]; generic.line != 42 || generic.endLine != 48 {
		t.Errorf("Generic lines %d-%d", generic.line, generic.endLine)
	}
}

func TestSignatureKey(t *testing.T) {
	for _, pair := range [][2]string{
		{"func Dispose(ctx context.Context) error", "func Dispose(c context.Context) (err error)"},
		{"func F(a, b int, s ...string) (int, error)", "func F(x int, y int, rest ...string) (n int, err error)"},
		{"func G([]int, map[string]int)", "func G(xs []int, m map[string]int)"},
		{"func H(v List[int])", "func H(List[int])"},
	} {
		if a, b := signatureKey(pair[0]), signatureKey(pair[1]); a != b {
			t.Errorf("keys differ:\n %q -> %q\n %q -> %q", pair[0], a, pair[1], b)
		}
	}

	if signatureKey("func F(a int)") == signatureKey("func F(a string)") {
		t.Error("different types must give different keys")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, root := range []string{"../../..", "testdata/injection"} {
		t.Run(root, func(t *testing.T) {
			service := newService(t, root)
			dumped, err := service.Dump("")
			if err != nil {
				t.Fatal(err)
			}

			if again, _ := service.Dump(""); dumped != again {
				t.Fatal("two dumps differ")
			}

			parsed, err := service.ParseGraph(dumped)
			if err != nil {
				t.Fatal(err)
			}

			extracted, _ := service.Extract()
			if differences := graph.Compare(extracted, parsed); len(differences) > 0 {
				t.Fatalf("round trip lost %d thing(s):\n%s", len(differences), strings.Join(differences[:min(len(differences), 20)], "\n"))
			}
		})
	}
}

func TestFixtureViolation(t *testing.T) {
	report, err := newService(t, "testdata/injection").Check("")
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Violations) != 1 {
		t.Fatalf("want one violation, got %+v", report.Violations)
	}

	violation := report.Violations[0]
	reference := violation.References[0]
	if violation.From != "go:internal/graph" || violation.To != "go:adapters/reflectx" ||
		reference.From != "go:internal/graph:typeName" || reference.To != "go:adapters/reflectx:TypeName" ||
		reference.File != "internal/graph/names.go" || reference.Line != 6 {
		t.Fatalf("wrong violation %+v", violation)
	}

	if len(report.Failures) == 0 {
		t.Fatal("a layer violation must fail the check")
	}
}

func TestFixtureGraph(t *testing.T) {
	g, err := newService(t, "testdata/injection").Extract()
	if err != nil {
		t.Fatal(err)
	}

	if resolve := g.Symbol("go:injection:container.Resolve"); resolve == nil || !resolve.Pointer || resolve.Contract {
		t.Fatalf("container.Resolve: %+v", resolve)
	}

	if method := g.Symbol("go:injection:Container.Resolve"); method == nil || !method.Contract || method.Signature != "func Resolve(ctx context.Context, k Key) (any, error)" {
		t.Fatalf("Container.Resolve: %+v", method)
	}

	if key := g.Symbol("go:injection:Key"); len(key.Fields) != 2 || !key.Fields[0].Contract || key.Fields[1].Contract {
		t.Fatalf("Key fields: %+v", key.Fields)
	}

	for _, want := range []graph.Edge{
		{From: "go:injection:container", To: "go:injection:Container", Kind: graph.EdgeImplements},
		{From: "go:injection:container.Resolve", To: "go:internal/graph:Graph.Order", Kind: graph.EdgeCall},
		{From: "go:injection:container.Resolve", To: "go:injection:resolveLocked", Kind: graph.EdgeCall},
		{From: "go:internal/graph:Graph.Order", To: "go:internal/graph:topoSort", Kind: graph.EdgeCall},
		{From: "go:injection:New", To: "go:internal/graph:New", Kind: graph.EdgeCall},
	} {
		if !slices.ContainsFunc(g.Edges, func(edge graph.Edge) bool {
			return edge.From == want.From && edge.To == want.To && edge.Kind == want.Kind
		}) {
			t.Errorf("missing edge %s -%s-> %s", want.From, want.Kind, want.To)
		}
	}
}

func TestDesignScaffoldAndCheck(t *testing.T) {
	service := newService(t, "testdata/injection")
	if _, err := service.DesignNew("probe", []string{"go:injection"}); err != nil {
		t.Fatal(err)
	}

	report, err := service.DesignCheck("probe")
	if err != nil {
		t.Fatal(err)
	}

	if report.Failures != 0 {
		t.Fatalf("a fresh scaffold must pass: %+v", report.Items)
	}

	if _, err := service.DesignNew("probe", nil); !errors.Is(err, filesystem.ErrDesignExists) {
		t.Fatalf("second scaffold must fail with ErrDesignExists, got %v", err)
	}
}

func TestDialect(t *testing.T) {
	dialect := NewDialect()
	parent := &app.Declaration{Kind: graph.KindInterface, Name: "Scope", Contract: true}
	designed, err := dialect.Declare("func Dispose(ctx context.Context) error", parent)
	if err != nil {
		t.Fatal(err)
	}

	built, _ := dialect.Declare("func Dispose(c context.Context) (err error)", parent)
	if designed.Key != built.Key || designed.Kind != graph.KindMethod || !designed.Contract {
		t.Fatalf("keys differ: %q vs %q", designed.Key, built.Key)
	}

	field, _ := dialect.Declare("Addr string `json:\"addr\"`", &app.Declaration{Kind: graph.KindType, Contract: true})
	if field.Kind != app.KindField || !field.Contract {
		t.Fatalf("field: %+v", field)
	}

	for _, bad := range []string{"not go at all", "func (", "type"} {
		if _, err := dialect.Declare(bad, nil); !errors.Is(err, ErrNotDeclaration) {
			t.Errorf("%q: want ErrNotDeclaration, got %v", bad, err)
		}
	}
}

func newService(t *testing.T, root string) *app.Service {
	t.Helper()
	return app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		nil, filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()))
}

func TestBuildVariants(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("go.mod", "module example.com/kernel\n")
	write("kernel.go", "package kernel\n\nfunc scalar() {}\n\nfunc vector() {}\n\n// Dot is the portable kernel.\nfunc Dot() float64 { scalar(); return 0 }\n")
	write("kernel_amd64.go", "//go:build amd64\n\npackage kernel\n\n// Dot is the SIMD kernel.\nfunc Dot() float64 { vector(); return 1 }\n")

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	dot := g.Symbol("go:.:Dot")
	if dot == nil || dot.File != "kernel.go" || len(dot.Variants) != 1 || dot.Variants[0].File != "kernel_amd64.go" {
		t.Fatalf("Dot: %+v", dot)
	}

	calls := map[string]string{}
	for _, edge := range g.Edges {
		if edge.From == dot.ID {
			calls[edge.To] = edge.File
		}
	}

	if calls["go:.:scalar"] != "kernel.go" || calls["go:.:vector"] != "kernel_amd64.go" {
		t.Fatalf("variant edges: %v", calls)
	}

	service := newService(t, root)
	dumped, err := service.Dump("")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := service.ParseGraph(dumped)
	if err != nil {
		t.Fatal(err)
	}

	if differences := graph.Compare(g, parsed); len(differences) > 0 {
		t.Fatalf("round trip lost:\n%s\n%s", strings.Join(differences, "\n"), dumped)
	}

	_ = os.Remove(filepath.Join(root, "kernel_amd64.go"))
	portable, _ := NewExtractor().Extract(root)
	if portable.Symbol(dot.ID).Hash == dot.Hash {
		t.Fatal("the hash must cover every variant")
	}
}

func TestImportOfRootPackage(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":              "module example.com/lib\n",
		"lib.go":              "package lib\n\nfunc New() int { return 1 }\n",
		"cmd/tool.go":         "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.New() }\n",
		"cmd/other.go":        "package main\n",
		"examples/go.mod":     "module example.com/lib/examples\n",
		"examples/example.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.New() }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	if edges := g.ModuleEdges(); len(edges) != 1 || edges[0].From != "go:cmd" || edges[0].To != "go:." {
		t.Fatalf("an import of the root package must count, and a nested module must not: %+v", edges)
	}

	if !g.Module("go:cmd").Entry || g.Module("go:.").Entry {
		t.Fatal("only package main is an entry point")
	}
}
