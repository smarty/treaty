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
		"_":            "var _",
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

	if len(file.declarations) != 17 {
		t.Errorf("want 17 declarations, got %d", len(file.declarations))
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
		nil, filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
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
		"examples/example.go": "package main\n\nimport (\n\t\"example.com/lib\"\n\t\"example.com/tools/gen\"\n)\n\nfunc main() { lib.New(); gen.Run() }\n",
		"tools/go.mod":        "module example.com/tools\n",
		"tools/gen/gen.go":    "package gen\n\nfunc Run() {}\n",
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

	var edges []string
	for _, edge := range g.ModuleEdges() {
		edges = append(edges, edge.From+" -> "+edge.To)
	}

	if want := "go:cmd -> go:., go:examples -> go:., go:examples -> go:tools/gen"; strings.Join(edges, ", ") != want {
		t.Fatalf("imports of the root package and across nested modules must count: got %v, want %s", edges, want)
	}

	if !g.Module("go:cmd").Entry || g.Module("go:.").Entry {
		t.Fatal("only package main is an entry point")
	}

	if g.Module("go:.").Manifest != "go.mod" || g.Module("go:examples").Manifest != "examples/go.mod" || g.Module("go:cmd").Manifest != "" || g.Module("go:tools/gen").Manifest != "" {
		t.Fatalf("only a package beside a go.mod has a manifest: %q %q %q", g.Module("go:.").Manifest, g.Module("go:examples").Manifest, g.Module("go:cmd").Manifest)
	}
}

func TestHiddenDependencies(t *testing.T) {
	const api = "package api\n\ntype Handler interface{ Serve() }\n\nfunc NewServer() int { return 1 }\n\nvar Default = 1\n"
	for name, storage := range map[string]string{
		"call in a function":   "package storage\n\nimport \"example.com/shop/internal/api\"\n\nfunc F() int { return api.NewServer() }\n",
		"aliased import":       "package storage\n\nimport a2 \"example.com/shop/internal/api\"\n\nfunc F() int { return a2.NewServer() }\n",
		"type in a body":       "package storage\n\nimport \"example.com/shop/internal/api\"\n\nfunc F() { var h api.Handler; _ = h }\n",
		"value":                "package storage\n\nimport \"example.com/shop/internal/api\"\n\nvar x = api.Default\n",
		"interface assertion":  "package storage\n\nimport \"example.com/shop/internal/api\"\n\ntype T struct{}\n\nfunc (T) Serve() {}\n\nvar _ api.Handler = T{}\n",
		"blank value":          "package storage\n\nimport \"example.com/shop/internal/api\"\n\nvar _ = api.NewServer\n",
		"blank import":         "package storage\n\nimport _ \"example.com/shop/internal/api\"\n",
		"dot import":           "package storage\n\nimport . \"example.com/shop/internal/api\"\n\nfunc F() int { return NewServer() }\n",
		"init function":        "package storage\n\nimport \"example.com/shop/internal/api\"\n\nfunc init() { _ = api.NewServer() }\n",
		"second init function": "package storage\n\nimport \"example.com/shop/internal/api\"\n\nfunc init() {}\n\nfunc init() { _ = api.NewServer() }\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for file, text := range map[string]string{
				"go.mod":                   "module example.com/shop\n",
				"treaty.yaml":              "architecture: layered\nlayers:\n  presentation: [\"internal/api/**\"]\n  data: [\"internal/storage/**\"]\nrules:\n  fail_on: [layer_violation]\n",
				"internal/api/api.go":      api,
				"internal/storage/file.go": storage,
			} {
				path := filepath.Join(root, filepath.FromSlash(file))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			service := newService(t, root)
			report, err := service.Check("")
			if err != nil {
				t.Fatal(err)
			}

			if len(report.Violations) != 1 || report.Violations[0].From != "go:internal/storage" || report.Violations[0].To != "go:internal/api" {
				t.Fatalf("storage → api must be a violation: %+v", report.Violations)
			}

			if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "no base") {
				t.Fatalf("a check with no base says it compared nothing: %v", report.Notes)
			}

			if file, line, _ := report.Violations[0].First(); file != "internal/storage/file.go" || line == 0 {
				t.Fatalf("the violation must point into the file: %s:%d", file, line)
			}

			dumped, err := service.Dump("")
			if err != nil {
				t.Fatal(err)
			}

			parsed, err := service.ParseGraph(dumped)
			if err != nil {
				t.Fatal(err)
			}

			extracted, _ := service.Extract()
			if differences := graph.Compare(extracted, parsed); len(differences) > 0 {
				t.Fatalf("round trip lost:\n%s\n%s", strings.Join(differences, "\n"), dumped)
			}

			for _, finding := range report.Findings {
				if finding.Kind == "variant_mismatch" {
					t.Fatalf("repeated init or blank declarations are not build variants: %+v", finding)
				}
			}
		})
	}
}

func TestNavigation(t *testing.T) {
	root := t.TempDir()
	for file, text := range map[string]string{
		"go.mod":                     "module example.com/shop\n",
		"treaty.yaml":                "architecture: layered\nlayers:\n  presentation: [\"internal/api/**\"]\n  data: [\"internal/storage/**\"]\n",
		"internal/storage/books.go":  "package storage\n\n// Book is one book.\ntype Book struct{ Title string }\n\nfunc Load() Book { return Book{} }\n",
		"internal/storage/orders.go": "package storage\n\nfunc Orders() int { return 0 }\n",
		"internal/api/api.go":        "package api\n\nimport \"example.com/shop/internal/storage\"\n\nfunc Get() string { return storage.Load().Title }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := newService(t, root)
	file, err := service.Slice("internal/storage/books.go")
	if err != nil {
		t.Fatal(err)
	}

	if file.TaskScope.File != "internal/storage/books.go" || len(file.Symbols) != 2 || file.Symbols[0].Line != 4 {
		t.Fatalf("a file slice lists the file's declarations with lines: %+v", file)
	}

	if len(file.Neighbors) != 1 || file.Neighbors[0].Symbol != "go:internal/api:Get" || file.Neighbors[0].Relation != "called_by" || file.Neighbors[0].File != "internal/api/api.go" || file.Neighbors[0].Line != 5 {
		t.Fatalf("a file slice's neighbors come from other files, with locations: %+v", file.Neighbors)
	}

	if prefixed, err := service.Slice("go:internal/storage/books.go"); err != nil || prefixed.TaskScope.File != file.TaskScope.File {
		t.Fatalf("a file may carry its language prefix: %v %+v", err, prefixed.TaskScope)
	}

	module, err := service.Slice("go:internal/storage")
	if err != nil {
		t.Fatal(err)
	}

	if len(module.Files) != 2 || module.Files[0].File != "internal/storage/books.go" || module.Files[0].Symbols != 2 || module.Files[1].Contracts != 1 {
		t.Fatalf("a module slice lists its files: %+v", module.Files)
	}

	symbol, err := service.Slice("go:internal/storage:Load")
	if err != nil {
		t.Fatal(err)
	}

	if symbol.TaskScope.Line != 6 || symbol.TaskScope.EndLine != 6 {
		t.Fatalf("a symbol slice gives the symbol's lines: %+v", symbol.TaskScope)
	}

	if found, err := service.Find("load", ""); err != nil || !strings.Contains(found, "internal/storage/books.go:6") {
		t.Fatalf("find: %v %q", err, found)
	}

	if impact, err := service.Impact("go:internal/storage:Load"); err != nil || !strings.Contains(impact, "go:internal/api") {
		t.Fatalf("impact: %v %q", err, impact)
	}

	if allowed, err := service.Allowed("internal/storage", "internal/api"); err != nil || !strings.HasPrefix(allowed, "not allowed") {
		t.Fatalf("allowed: %v %q", err, allowed)
	}

	if overview, err := service.Overview(""); err != nil || !strings.Contains(overview, "baseline none") {
		t.Fatalf("overview: %v %q", err, overview)
	}
}

func TestDocComment(t *testing.T) {
	file := strings.Split("package x\n\n// Load reads\n// the file.\nfunc Load() {}\n\n//go:noinline\n// Fast is quick.\nfunc Fast() {}\n\n/*\n * Block is\n * a comment.\n */\nfunc Block() {}\n\nvar y = 1 // trailing\nfunc Bare() {}\n\nconst (\n\t// A is first.\n\tA = 1\n)\n", "\n")
	for line, want := range map[int]struct {
		text  string
		start int
	}{5: {"Load reads\nthe file.", 3}, 9: {"Fast is quick.", 7}, 15: {"Block is\na comment.", 11}, 18: {"", 0}, 22: {"A is first.", 21}} {
		if text, start := docComment(file, line); text != want.text || start != want.start {
			t.Errorf("line %d: got %q from %d, want %q from %d", line, text, start, want.text, want.start)
		}
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(strings.Join(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	if doc := g.Symbol("go:.:Load").Doc; doc != "Load reads\nthe file." {
		t.Fatalf("the extractor records documentation: %q", doc)
	}
}

func TestShortNamesSourceAndSummaries(t *testing.T) {
	root := t.TempDir()
	for file, text := range map[string]string{
		"go.mod":                    "module example.com/shop\n",
		"treaty.yaml":               "architecture: layered\nlayers:\n  presentation: [\"internal/api/**\"]\n  data: [\"internal/storage/**\"]\n",
		"internal/storage/books.go": "package storage\n\n// Store keeps books.\ntype Store struct{}\n\n// Load reads a book.\nfunc (s *Store) Load() int {\n\treturn 1\n}\n",
		"internal/storage/other.go": "package storage\n\nfunc Other() {}\n",
		"internal/api/api.go":       "package api\n\nimport \"example.com/shop/internal/storage\"\n\nfunc Get(s *storage.Store) int { return s.Load() }\n\nfunc Other() {}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := newService(t, root)
	for target, want := range map[string]string{
		"Store.Load":       "go:internal/storage:Store.Load",
		"Load":             "go:internal/storage:Store.Load",
		"storage:Store":    "go:internal/storage:Store",
		"books.go":         "internal/storage/books.go",
		"internal/storage": "go:internal/storage",
	} {
		slice, err := service.Slice(target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}

		got := slice.TaskScope.Symbol
		if got == "" {
			got = slice.TaskScope.File
		}

		if got == "" {
			got = slice.TaskScope.Module
		}

		if got != want {
			t.Errorf("%s resolved to %s, want %s", target, got, want)
		}
	}

	if _, err := service.Slice("Other"); !errors.Is(err, app.ErrAmbiguousTarget) || !strings.Contains(err.Error(), "go:internal/api:Other") {
		t.Fatalf("an ambiguous name lists its candidates: %v", err)
	}

	if _, err := service.Impact("Lod"); !errors.Is(err, app.ErrUnknownTarget) {
		t.Fatalf("an unknown name is unknown: %v", err)
	}

	if impact, err := service.Impact("Load"); err != nil || !strings.Contains(impact, "go:internal/api") {
		t.Fatalf("impact takes short names: %v %q", err, impact)
	}

	source, err := service.Source("Load", false)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(source, "internal/storage/books.go:6-9") || !strings.Contains(source, "     6\t// Load reads a book.") || !strings.Contains(source, "     9\t}") {
		t.Fatalf("a symbol's source runs from its documentation to its last line, numbered:\n%s", source)
	}

	if lines, err := service.Source("books.go:3-4", false); err != nil || strings.Count(lines, "\n") != 3 {
		t.Fatalf("a file range prints just those lines: %v\n%s", err, lines)
	}

	if _, err := service.Source("books.go:3-40", false); !errors.Is(err, app.ErrUnknownTarget) {
		t.Fatalf("a range past the end of the file is refused: %v", err)
	}

	if _, err := service.Source("internal/storage", false); !errors.Is(err, app.ErrUnknownTarget) {
		t.Fatalf("a module has no single source: %v", err)
	}

	slice, _ := service.Slice("books.go")
	if text := slice.Text(); !strings.Contains(text, "symbols (2):") || !strings.Contains(text, "7-9  method  Store.Load") || !strings.Contains(text, "called_by  go:internal/api:Get  internal/api/api.go:5") {
		t.Fatalf("a text slice has one line per entry:\n%s", text)
	}

	report, err := service.Check("")
	if err != nil {
		t.Fatal(err)
	}

	if summary := report.Summary(); !strings.HasPrefix(summary, "PASS\n") || !strings.Contains(summary, "note: no base given") {
		t.Fatalf("summary:\n%s", summary)
	}
}
