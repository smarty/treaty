package python

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

const tricky = `#!/usr/bin/env python
import os.path, json as j
from . import sibling
from ..pkg.mod import (a, b as c)
from x import *

try:
    import fast
except ImportError:
    fast = None

if True:
    def conditional(): pass

async def fetch[T](url: str, *args: int, retries=3, **kwargs) -> list[T]:
    """Fetch it."""
    text = f"{url!r:>{retries}} {{literal}}"
    return [x for x in args]

@decorator(
    arg=1,
)
class Thing(Base, metaclass=Meta):
    count: int = 0
    name = "thing"
    __slots__ = ()

    def method(self, a, /, b=1, *, c) -> None: return None

    class Nested:
        pass

type Alias = dict[str, int]
A, (B, C) = 1, (2, 3)
D = E = 4
obj.attr = 5
__all__ = ["fetch", "Thing"]
`

func TestParseTricky(t *testing.T) {
	file := parseFile([]byte(tricky))
	var imports []string
	for _, spec := range file.imports {
		imports = append(imports, strings.Repeat(".", spec.level)+spec.module)
	}

	if want := []string{"os.path", "json", ".", "..pkg.mod", "x", "fast"}; !slices.Equal(imports, want) {
		t.Errorf("imports: got %q, want %q", imports, want)
	}

	if spec := file.imports[3]; len(spec.names) != 2 || spec.names[1] != (alias{"b", "c"}) || !file.imports[4].star || file.imports[1].as != "j" {
		t.Errorf("import names misread: %+v", file.imports)
	}

	byName := map[string]*declaration{}
	for _, declaration := range file.declarations {
		byName[declaration.name] = declaration
	}

	for name, want := range map[string]string{
		"conditional":  "def conditional()",
		"fetch":        "async def fetch[T](url: str, *args: int, retries=3, **kwargs) -> list[T]",
		"Thing":        "class Thing(Base, metaclass=Meta)",
		"Thing.method": "def method(self, a, /, b=1, *, c) -> None",
		"Alias":        "type Alias = dict[str, int]",
		"A":            "A = 1, (2, 3)",
		"C":            "C = 1, (2, 3)",
		"D":            "D = 4",
		"E":            "E = 4",
		"fast":         "fast = None",
	} {
		declaration := byName[name]
		if declaration == nil {
			t.Errorf("%s: missing", name)
			continue
		}

		if declaration.signature != want {
			t.Errorf("%s: got %q, want %q", name, declaration.signature, want)
		}
	}

	if byName["Thing.Nested"] != nil || byName["obj"] != nil || byName["__all__"] != nil {
		t.Error("nested classes, attribute targets and __all__ are not declarations")
	}

	if got := byName["fetch"].key; got != "async fetch[T](str,*int,_?,**_)->list[T]" {
		t.Errorf("fetch key: %q", got)
	}

	if got := byName["Thing.method"].key; got != "method(_,/,_?,*,_)->None" {
		t.Errorf("method key: %q", got)
	}

	if byName["fetch"].doc != "Fetch it." {
		t.Errorf("docstring: %q", byName["fetch"].doc)
	}

	var fieldTexts []string
	for _, field := range byName["Thing"].fields {
		fieldTexts = append(fieldTexts, field.Text)
	}

	if want := []string{"count: int", "name"}; !slices.Equal(fieldTexts, want) {
		t.Errorf("fields: got %q, want %q", fieldTexts, want)
	}

	if len(byName["Thing"].bases) != 1 || baseName(byName["Thing"].bases[0]) != "Base" {
		t.Errorf("bases: %v", byName["Thing"].bases)
	}

	if !file.hasAll || !slices.Equal(file.all, []string{"fetch", "Thing"}) {
		t.Errorf("__all__: %q", file.all)
	}
}

func TestLexer(t *testing.T) {
	for _, test := range []struct {
		name string
		src  string
		want []string
	}{
		{"prefixes", `rb"\d" u'x' br'y'`, []string{`rb"\d"`, `u'x'`, `br'y'`}},
		{"triple quotes", "'''a\n'b'\n''' x", []string{"'''a\n'b'\n'''", "x"}},
		{"f-string", `f"a{b + c!r:>{w}}d{{e}}"`, []string{`f"a{`, "b", "+", "c", "!", "r", ":", ">", "{", "w", "}", `}d{{e}}"`}},
		{"operators", "a //= b ** c -> d := e", []string{"a", "//=", "b", "**", "c", "->", "d", ":=", "e"}},
		{"comment", "a # b\nc", []string{"a", "c"}},
	} {
		var got []string
		for _, current := range lex([]byte(test.src)) {
			got = append(got, current.text)
		}

		if !slices.Equal(got, test.want) {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}

	lines := logicalLines(lex([]byte("a = (1,\n  2)\nb = 1 + \\\n  2\n    c\n")))
	if len(lines) != 3 || lines[2].indent != 4 || lines[1].tokens[len(lines[1].tokens)-1].text != "2" {
		t.Errorf("logical lines: %+v", lines)
	}
}

func TestFixtureGraph(t *testing.T) {
	g, err := NewExtractor().Extract("testdata/library")
	if err != nil {
		t.Fatal(err)
	}

	var modules []string
	for _, module := range g.Modules {
		modules = append(modules, module.ID)
	}

	want := []string{"py:src/library", "py:src/library/adapters", "py:src/library/adapters/_internal", "py:src/library/app", "py:src/library/cli", "py:src/library/domain"}
	if !slices.Equal(modules, want) {
		t.Errorf("modules: got %q, want %q", modules, want)
	}

	if module := g.Module("py:src/library/cli"); module.Entry {
		t.Error("a package with __main__.py and a module that is not a script can still be imported")
	}

	if module := g.Module("py:src/library/adapters/_internal"); !module.Private {
		t.Error("an _internal package is private")
	}

	for _, missing := range []string{"py:src/library/domain:test_ignored", "py:.venv/lib:venv", "py:build:built"} {
		if g.Symbol(missing) != nil {
			t.Errorf("%s should not be extracted", missing)
		}
	}

	for id, want := range map[string]struct {
		kind     string
		contract bool
	}{
		"py:src/library/domain:Book":                 {graph.KindType, true},
		"py:src/library/domain:Book.short_title":     {graph.KindMethod, true},
		"py:src/library/domain:Book.label":           {graph.KindMethod, true},
		"py:src/library/domain:Shelf.__init__":       {graph.KindMethod, true},
		"py:src/library/domain:Shelf._reindex":       {graph.KindMethod, false},
		"py:src/library/domain:_slug":                {graph.KindFunction, false},
		"py:src/library/domain:MAX_TITLE":            {graph.KindValue, true},
		"py:src/library/domain:Catalog":              {graph.KindInterface, true},
		"py:src/library/domain:Catalog.find":         {graph.KindMethod, true},
		"py:src/library/app:Lending.lend":            {graph.KindMethod, true},
		"py:src/library/cli:__main__/main":           {graph.KindFunction, true},
		"py:src/library/cli:commands/main":           {graph.KindFunction, true},
		"py:src/library/adapters:MemoryCatalog":      {graph.KindType, true},
		"py:src/library/adapters/_internal:key":      {graph.KindFunction, true},
		"py:src/library:__version__":                 {graph.KindValue, true},
		"py:src/library/adapters:MemoryCatalog.find": {graph.KindMethod, true},
	} {
		symbol := g.Symbol(id)
		if symbol == nil {
			t.Errorf("%s: missing", id)
			continue
		}

		if symbol.Kind != want.kind || symbol.Contract != want.contract {
			t.Errorf("%s: got %s contract=%t, want %s contract=%t", id, symbol.Kind, symbol.Contract, want.kind, want.contract)
		}
	}

	book := g.Symbol("py:src/library/domain:Book")
	if book.Doc != "A book on a shelf.\n\nBooks are values." {
		t.Errorf("docstring: %q", book.Doc)
	}

	if len(book.Fields) != 4 || !book.Fields[0].Contract || book.Fields[3].Contract {
		t.Errorf("Book fields: %+v", book.Fields)
	}

	if doc := g.Symbol("py:src/library/domain:Shelf.__init__").Doc; doc != "Shelves hold books." {
		t.Errorf("comment doc: %q", doc)
	}

	var shelfFields []string
	for _, field := range g.Symbol("py:src/library/domain:Shelf").Fields {
		shelfFields = append(shelfFields, field.Text)
	}

	if want := []string{"books: list[Book]", "_count"}; !slices.Equal(shelfFields, want) {
		t.Errorf("Shelf fields from __init__: got %q, want %q", shelfFields, want)
	}

	edges := map[string]bool{}
	for _, edge := range g.Edges {
		edges[edge.From+" -"+edge.Kind+"-> "+edge.To] = true
	}

	for _, want := range []string{
		"py:src/library/adapters:MemoryCatalog -embeds-> py:src/library/domain:Catalog",
		"py:src/library/adapters:MemoryCatalog.__init__ -type-use-> py:src/library/domain:Book",
		"py:src/library/adapters:MemoryCatalog.find -call-> py:src/library/adapters:key",
		"py:src/library/app:Lending.lend -type-use-> py:src/library/domain:Book",
		"py:src/library/app:Lending.lend -call-> py:src/library/domain:Shelf.empty",
		"py:src/library/app:Lending.lend -call-> py:src/library/domain:Shelf.add",
		"py:src/library/app:Lending.__init__ -type-use-> py:src/library/domain:Catalog",
		"py:src/library/app:default_lending -type-use-> py:src/library/adapters:MemoryCatalog",
		"py:src/library/cli:__main__/main -call-> py:src/library/app:default_lending",
		"py:src/library/domain:Book.label -call-> py:src/library/domain:Book.short_title",
		"py:src/library/domain:Book.label -type-use-> py:src/library/domain:MAX_TITLE",
		"py:src/library/domain:Catalog.find -type-use-> py:src/library/domain:Book",
	} {
		if !edges[want] {
			t.Errorf("missing edge %s", want)
		}
	}

	if edges["py:src/library/app:Lending.lend -call-> py:src/library/domain:Catalog.find"] {
		t.Error("catalog.find has two candidates, so it must not resolve")
	}

	imports := map[string]bool{}
	for _, item := range g.Imports {
		imports[item.From+" -> "+item.To] = true
	}

	for _, want := range []string{"py:src/library/app -> py:src/library/domain", "py:src/library/app -> py:src/library/adapters", "py:src/library/adapters -> py:src/library/adapters/_internal", "py:src/library/cli -> py:src/library/app"} {
		if !imports[want] {
			t.Errorf("missing import %s", want)
		}
	}
}

func TestVendoredPackages(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"plugin/main.py":              "from vendored.client import Client\n\ndef run():\n    return Client()\n",
		"plugin/vendored/__init__.py": "",
		"plugin/vendored/client.py":   "class Client:\n    pass\n",
		"plugin/a/util/__init__.py":   "def help(): pass\n",
		"plugin/b/util/__init__.py":   "def help(): pass\n",
		"plugin/other.py":             "import util\n\ndef go():\n    return util.help()\n",
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	var edges []string
	for _, edge := range g.Edges {
		edges = append(edges, edge.From+" -> "+edge.To)
	}

	// A package found in one place resolves; one found in two does not.
	if want := []string{"py:plugin:run -> py:plugin/vendored:Client"}; !slices.Equal(edges, want) {
		t.Errorf("edges: got %q, want %q", edges, want)
	}
}

func TestEntryPackages(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"scripts/run.py":      "def main(): pass\n\nif __name__ == \"__main__\":\n    main()\n",
		"scripts/__main__.py": "print(1)\n",
		"lib/core.py":         "def demo(): pass\n\nif __name__ == \"__main__\":\n    demo()\n",
		"lib/more.py":         "def more(): pass\n",
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	if !g.Module("py:scripts").Entry || g.Module("py:lib").Entry {
		t.Error("only a package made of scripts is an entry point; one demo guard leaves a library importable")
	}
}

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	copyTree(t, "testdata/library", root)
	if err := os.Remove(filepath.Join(root, "src", "library", "cli", "commands.py")); err != nil {
		t.Fatal(err)
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

	extracted, _ := service.Extract()
	if differences := graph.Compare(extracted, parsed); len(differences) > 0 {
		t.Fatalf("round trip lost %d thing(s):\n%s", len(differences), strings.Join(differences[:min(len(differences), 20)], "\n"))
	}
}

func TestFixtureCheck(t *testing.T) {
	report, err := newService(t, "testdata/library").Check("")
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, violation := range report.Violations {
		violations = append(violations, violation.From+" -> "+violation.To)
	}

	// The application may not reach a driven adapter, and adapters may not
	// reach each other, a helper package of their own included.
	if want := []string{"py:src/library/adapters -> py:src/library/adapters/_internal", "py:src/library/app -> py:src/library/adapters"}; !slices.Equal(violations, want) {
		t.Errorf("violations: got %q, want %q", violations, want)
	}
}

func TestDialect(t *testing.T) {
	dialect := NewDialect()
	for text, want := range map[string]app.Declaration{
		"def resolve(name: str) -> Thing": {Kind: graph.KindFunction, Name: "resolve", Contract: true, Key: "resolve(str)->Thing"},
		"async def _load(a, b=1)":         {Kind: graph.KindFunction, Name: "_load", Key: "async _load(_,_?)"},
		"class Store(Base, Generic[T])":   {Kind: graph.KindType, Name: "Store", Contract: true, Key: "class Store(Base, Generic[T])"},
		"class Port(typing.Protocol)":     {Kind: graph.KindInterface, Name: "Port", Contract: true, Key: "class Port(typing.Protocol)"},
		"LIMIT: int = 100":                {Kind: graph.KindValue, Name: "LIMIT", Contract: true, Key: "LIMIT: int"},
		"type Id = int | str":             {Kind: graph.KindType, Name: "Id", Contract: true, Key: "type Id = int | str"},
	} {
		got, err := dialect.Declare(text, nil)
		if err != nil || got != want {
			t.Errorf("%q: got %+v (%v), want %+v", text, got, err, want)
		}
	}

	class := &app.Declaration{Kind: graph.KindType, Name: "Store", Contract: true}
	for text, want := range map[string]app.Declaration{
		"def save(self, item: T) -> None": {Kind: graph.KindMethod, Name: "save", Contract: true, Key: "save(T)->None"},
		"def _flush(self)":                {Kind: graph.KindMethod, Name: "_flush", Key: "_flush()"},
		"def __len__(self) -> int":        {Kind: graph.KindMethod, Name: "__len__", Contract: true, Key: "__len__()->int"},
		"size: int":                       {Kind: app.KindField, Name: "size", Contract: true, Key: "size: int"},
		"_count":                          {Kind: app.KindField, Name: "_count", Key: "_count"},
	} {
		got, err := dialect.Declare(text, class)
		if err != nil || got != want {
			t.Errorf("%q in a class: got %+v (%v), want %+v", text, got, err, want)
		}
	}

	for _, bad := range []string{"return x", "x + 1", "", "import os"} {
		if _, err := dialect.Declare(bad, nil); !errors.Is(err, ErrNotDeclaration) {
			t.Errorf("%q: want ErrNotDeclaration, got %v", bad, err)
		}
	}

	if dialect.ModulePath("src/app/lending.py") != "src/app" || dialect.ModulePath("src/app") != "src/app" || !dialect.IsFile("a.py") || dialect.IsFile("a.go") {
		t.Error("paths misread")
	}
}

func TestCompatible(t *testing.T) {
	dialect := NewDialect()
	for _, test := range []struct {
		kind, before, after string
		want                bool
	}{
		{graph.KindFunction, "def f(a: str) -> None", "def f(renamed: str) -> None", true},
		{graph.KindFunction, "def f(a: str) -> None", "def f(a: int) -> None", false},
		{graph.KindFunction, "def f(a)", "def f(a, b)", false},
		{graph.KindFunction, "def f(a, b)", "def f(a, /, b)", false},
		{graph.KindMethod, "def save(self, item: T)", "def save(self, thing: T)", true},
		{graph.KindValue, "LIMIT = 1", "LIMIT = 2", true},
		{graph.KindValue, "LIMIT: int = 1", "LIMIT: str = 1", false},
	} {
		before := &graph.Symbol{Kind: test.kind, Signature: test.before}
		after := &graph.Symbol{Kind: test.kind, Signature: test.after}
		if got := dialect.Compatible(before, after); got != test.want {
			t.Errorf("%q -> %q: got %t, want %t", test.before, test.after, got, test.want)
		}
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(current string, entry os.DirEntry, err error) error {
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

func newService(t *testing.T, root string) *app.Service {
	t.Helper()
	return app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		nil, filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
}
