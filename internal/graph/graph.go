// Package graph holds the contract graph: modules, symbols and the reference
// edges between them. It is pure data with no knowledge of any language.
package graph

import (
	"fmt"
	"sort"
	"strings"
)

const (
	EdgeCall       = "call"
	EdgeEmbeds     = "embeds"
	EdgeImplements = "implements"
	EdgeTypeUse    = "type-use"

	KindFunction  = "function"
	KindInterface = "interface"
	KindMethod    = "method"
	KindType      = "type"
	KindValue     = "value"

	LayerAdapter      = "adapter"
	LayerApplication  = "application"
	LayerComposition  = "composition"
	LayerContext      = "context"
	LayerDomain       = "domain"
	LayerNone         = "none"
	LayerShared       = "shared"
	LayerSlice        = "slice"
	LayerUnclassified = "unclassified"

	SideDriven  = "driven"
	SideDriving = "driving"
)

// Edge is one reference from a symbol to another symbol. Compare marks a
// reference that only compares the target, such as an operand of == or a
// case value; Literal one that builds it, such as T{...}; and Handled a
// reference to a function whose results never reach a return of the
// caller: a call whose error is checked and dropped, or a mention that does
// not call it. A language that cannot tell leaves them
// false.
type Edge struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Compare bool   `json:"compare,omitempty"`
	Literal bool   `json:"literal,omitempty"`
	Handled bool   `json:"handled,omitempty"`
}

// Field is one field of a struct-like type, kept as its declaration text.
type Field struct {
	Text     string `json:"text"`
	Contract bool   `json:"contract"`
}

// Graph is every module, symbol, edge and import of one source tree.
type Graph struct {
	Modules []*Module `json:"modules"`
	Symbols []*Symbol `json:"symbols"`
	Edges   []Edge    `json:"edges"`
	Imports []Import  `json:"imports,omitempty"`

	modules map[string]*Module
	symbols map[string]*Symbol
}

// Import is one file's import of another module. An import is a dependency
// even when nothing it provides is referenced, as with a blank import that
// runs the imported package's initialization.
type Import struct {
	From string `json:"from"`
	To   string `json:"to"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// Module is a unit of code placed into one layer, such as a Go package.
// Name is what the language calls it, and Entry marks a program's entry
// point, such as a Go main package, which nothing can import. Private marks
// a module only this repository can import, such as a Go internal package.
// Manifest is the language's module file in the module's directory, such as
// go.mod, when there is one. Slice is the
// root path of the vertical slice or bounded context holding the module, and
// Public marks a context's packages that other contexts may use.
type Module struct {
	ID       string   `json:"id"`
	Language string   `json:"language"`
	Path     string   `json:"path"`
	Name     string   `json:"name,omitempty"`
	Entry    bool     `json:"entry,omitempty"`
	Private  bool     `json:"private,omitempty"`
	Manifest string   `json:"manifest,omitempty"`
	Layer    string   `json:"layer"`
	Side     string   `json:"side,omitempty"`
	Slice    string   `json:"slice,omitempty"`
	Public   bool     `json:"public,omitempty"`
	Files    []string `json:"files"`
}

// ModuleEdge aggregates every reference and import from one module to
// another. An edge may have imports and no references.
type ModuleEdge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	References []Edge   `json:"references"`
	Imports    []Import `json:"imports,omitempty"`
}

// Symbol is a declaration: a function, method, interface, type or value.
// Doc is its documentation comment as plain text, without comment markers,
// and DocLine the line the comment starts on.
type Symbol struct {
	ID        string    `json:"id"`
	Module    string    `json:"module"`
	Name      string    `json:"name"`
	Parent    string    `json:"parent,omitempty"`
	Kind      string    `json:"kind"`
	File      string    `json:"file"`
	Line      int       `json:"line"`
	EndLine   int       `json:"end_line,omitempty"`
	Contract  bool      `json:"contract"`
	Signature string    `json:"signature"`
	Pointer   bool      `json:"pointer,omitempty"`
	Fields    []Field   `json:"fields,omitempty"`
	Hash      string    `json:"hash,omitempty"`
	Doc       string    `json:"doc,omitempty"`
	DocLine   int       `json:"doc_line,omitempty"`
	Variants  []Variant `json:"variants,omitempty"`
}

// Variant is another declaration of the same symbol: one in a file built
// under different constraints, such as a Go build tag, or a repeat of a name
// the language allows more than once, such as Go's init. The symbol keeps the
// first declaration; each other one is a variant. A repeat carries no
// signature, since it is not an alternative build of the first.
type Variant struct {
	File      string  `json:"file"`
	Line      int     `json:"line"`
	Signature string  `json:"signature"`
	Fields    []Field `json:"fields,omitempty"`
}

// New creates an empty graph.
//
// Returns:
//   - result: a graph ready for modules, symbols and edges.
func New() *Graph {
	return &Graph{
		modules: map[string]*Module{},
		symbols: map[string]*Symbol{},
	}
}

// AddEdge records a reference between two symbols.
//
// Parameters:
//   - edge: the reference to record.
func (this *Graph) AddEdge(edge Edge) {
	this.Edges = append(this.Edges, edge)
}

// AddImport records one file's import of another module.
//
// Parameters:
//   - item: the import to record.
func (this *Graph) AddImport(item Import) {
	this.Imports = append(this.Imports, item)
}

// AddModule records a module, returning the existing one when the id is known.
//
// Parameters:
//   - module: the module to record.
//
// Returns:
//   - result: the module stored in the graph.
func (this *Graph) AddModule(module *Module) *Module {
	if existing, ok := this.modules[module.ID]; ok {
		return existing
	}

	this.modules[module.ID] = module
	this.Modules = append(this.Modules, module)
	return module
}

// AddSymbol records a symbol. A duplicate id keeps the first declaration.
//
// Parameters:
//   - symbol: the symbol to record.
//
// Returns:
//   - result: the symbol stored in the graph.
func (this *Graph) AddSymbol(symbol *Symbol) *Symbol {
	if existing, ok := this.symbols[symbol.ID]; ok {
		return existing
	}

	this.symbols[symbol.ID] = symbol
	this.Symbols = append(this.Symbols, symbol)
	return symbol
}

// Clone copies the graph so that assigning layers to the copy leaves the
// original untouched. Symbols are shared, since nothing changes them after
// extraction.
//
// Returns:
//   - result: the copy.
func (this *Graph) Clone() *Graph {
	result := New()
	for _, module := range this.Modules {
		copied := *module
		copied.Files = append([]string(nil), module.Files...)
		result.AddModule(&copied)
	}

	for _, symbol := range this.Symbols {
		result.AddSymbol(symbol)
	}

	result.Edges = append([]Edge(nil), this.Edges...)
	result.Imports = append([]Import(nil), this.Imports...)
	return result
}

// Merge adds another graph's modules, symbols, edges and imports, so
// several extractors can build one graph. A module or symbol id already
// present keeps the first one.
//
// Parameters:
//   - other: the graph to add.
func (this *Graph) Merge(other *Graph) {
	for _, module := range other.Modules {
		this.AddModule(module)
	}

	for _, symbol := range other.Symbols {
		this.AddSymbol(symbol)
	}

	this.Edges = append(this.Edges, other.Edges...)
	this.Imports = append(this.Imports, other.Imports...)
}

// Module looks up a module by id.
//
// Parameters:
//   - id: the module id.
//
// Returns:
//   - result: the module, or nil when it does not exist.
func (this *Graph) Module(id string) *Module {
	return this.modules[id]
}

// ModuleEdges aggregates symbol edges and imports into edges between
// distinct modules.
//
// Returns:
//   - result: module edges sorted by source, then target.
func (this *Graph) ModuleEdges() []ModuleEdge {
	byPair := map[[2]string]*ModuleEdge{}
	pair := func(from, to string) *ModuleEdge {
		key := [2]string{from, to}
		if byPair[key] == nil {
			byPair[key] = &ModuleEdge{From: from, To: to}
		}

		return byPair[key]
	}

	for _, edge := range this.Edges {
		from, to := this.symbols[edge.From], this.symbols[edge.To]
		if from == nil || to == nil || from.Module == to.Module {
			continue
		}

		moduleEdge := pair(from.Module, to.Module)
		moduleEdge.References = append(moduleEdge.References, edge)
	}

	for _, item := range this.Imports {
		if this.modules[item.From] == nil || this.modules[item.To] == nil || item.From == item.To {
			continue
		}

		moduleEdge := pair(item.From, item.To)
		moduleEdge.Imports = append(moduleEdge.Imports, item)
	}

	result := make([]ModuleEdge, 0, len(byPair))
	for _, edge := range byPair {
		result = append(result, *edge)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].From != result[j].From {
			return result[i].From < result[j].From
		}

		return result[i].To < result[j].To
	})
	return result
}

// Normalize sorts everything into canonical order and drops duplicate edges
// and imports, and those whose ends are unknown.
func (this *Graph) Normalize() {
	sort.Slice(this.Modules, func(i, j int) bool { return this.Modules[i].ID < this.Modules[j].ID })
	for _, module := range this.Modules {
		sort.Strings(module.Files)
		module.Files = unique(module.Files)
	}

	sort.Slice(this.Symbols, func(i, j int) bool {
		a, b := this.Symbols[i], this.Symbols[j]
		if a.File != b.File {
			return a.File < b.File
		}

		if a.Line != b.Line {
			return a.Line < b.Line
		}

		return a.ID < b.ID
	})

	kept := this.Edges[:0]
	seen := map[Edge]bool{}
	for _, edge := range this.Edges {
		if seen[edge] || this.symbols[edge.From] == nil || this.symbols[edge.To] == nil || edge.From == edge.To {
			continue
		}

		seen[edge] = true
		kept = append(kept, edge)
	}

	this.Edges = kept
	sort.Slice(this.Edges, func(i, j int) bool { return edgeLess(this.Edges[i], this.Edges[j]) })
	imports := this.Imports[:0]
	seenImports := map[Import]bool{}
	for _, item := range this.Imports {
		if seenImports[item] || this.modules[item.From] == nil || this.modules[item.To] == nil || item.From == item.To {
			continue
		}

		seenImports[item] = true
		imports = append(imports, item)
	}

	this.Imports = imports
	sort.Slice(this.Imports, func(i, j int) bool { return importLess(this.Imports[i], this.Imports[j]) })
}

// Symbol looks up a symbol by id.
//
// Parameters:
//   - id: the symbol id.
//
// Returns:
//   - result: the symbol, or nil when it does not exist.
func (this *Graph) Symbol(id string) *Symbol {
	return this.symbols[id]
}

// SymbolsIn lists the symbols of one module in canonical order.
//
// Parameters:
//   - moduleID: the module id.
//
// Returns:
//   - result: the module's symbols.
func (this *Graph) SymbolsIn(moduleID string) []*Symbol {
	var result []*Symbol
	for _, symbol := range this.Symbols {
		if symbol.Module == moduleID {
			result = append(result, symbol)
		}
	}

	return result
}

// Compare reports every structural difference between two graphs, ignoring
// layers, sides, end lines and body hashes, which are not part of a graph's
// structure.
//
// Parameters:
//   - a: the first graph.
//   - b: the second graph.
//
// Returns:
//   - result: one line per difference, empty when the graphs are identical.
func Compare(a, b *Graph) []string {
	a.Normalize()
	b.Normalize()

	var result []string
	result = append(result, compareSets("module", moduleKeys(a), moduleKeys(b))...)
	result = append(result, compareSets("symbol", symbolKeys(a), symbolKeys(b))...)
	result = append(result, compareSets("edge", edgeKeys(a), edgeKeys(b))...)
	result = append(result, compareSets("import", importKeys(a), importKeys(b))...)
	return result
}

// ModuleID builds a module id from a language and a path.
//
// Parameters:
//   - language: the language prefix, such as go.
//   - path: the module path relative to the repository root.
//
// Returns:
//   - result: the module id.
func ModuleID(language, path string) string {
	return language + ":" + path
}

// SplitSymbolID separates a symbol id into its module id and qualified name.
//
// Parameters:
//   - id: the symbol id.
//
// Returns:
//   - moduleID: the module id, or empty when id is not a symbol id.
//   - name: the qualified name within the module.
func SplitSymbolID(id string) (moduleID string, name string) {
	index := strings.LastIndex(id, ":")
	if index <= 0 || strings.Count(id, ":") < 2 {
		return "", id
	}

	return id[:index], id[index+1:]
}

// SymbolID builds a symbol id from a module id and a qualified name.
//
// Parameters:
//   - moduleID: the module id.
//   - name: the qualified name, such as Container.Resolve.
//
// Returns:
//   - result: the symbol id.
func SymbolID(moduleID, name string) string {
	return moduleID + ":" + name
}

func compareSets(label string, a, b map[string]bool) []string {
	var result []string
	for key := range a {
		if !b[key] {
			result = append(result, fmt.Sprintf("- %s %s", label, key))
		}
	}

	for key := range b {
		if !a[key] {
			result = append(result, fmt.Sprintf("+ %s %s", label, key))
		}
	}

	sort.Strings(result)
	return result
}

func edgeKeys(g *Graph) map[string]bool {
	result := map[string]bool{}
	for _, edge := range g.Edges {
		result[fmt.Sprintf("%s -%s-> %s @%s:%d", edge.From, edge.Kind, edge.To, edge.File, edge.Line)] = true
	}

	return result
}

func edgeLess(a, b Edge) bool {
	if a.From != b.From {
		return a.From < b.From
	}

	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}

	if a.To != b.To {
		return a.To < b.To
	}

	if a.File != b.File {
		return a.File < b.File
	}

	return a.Line < b.Line
}

func importKeys(g *Graph) map[string]bool {
	result := map[string]bool{}
	for _, item := range g.Imports {
		result[fmt.Sprintf("%s -> %s @%s:%d", item.From, item.To, item.File, item.Line)] = true
	}

	return result
}

func importLess(a, b Import) bool {
	if a.From != b.From {
		return a.From < b.From
	}

	if a.To != b.To {
		return a.To < b.To
	}

	if a.File != b.File {
		return a.File < b.File
	}

	return a.Line < b.Line
}

func moduleKeys(g *Graph) map[string]bool {
	result := map[string]bool{}
	for _, module := range g.Modules {
		result[fmt.Sprintf("%s lang=%s path=%s files=%v", module.ID, module.Language, module.Path, module.Files)] = true
	}

	return result
}

func symbolKeys(g *Graph) map[string]bool {
	result := map[string]bool{}
	for _, s := range g.Symbols {
		key := fmt.Sprintf("%s kind=%s parent=%s file=%s:%d contract=%t ptr=%t sig=%q fields=%v variants=%v",
			s.ID, s.Kind, s.Parent, s.File, s.Line, s.Contract, s.Pointer, s.Signature, s.Fields, variantKeys(s.Variants))
		result[key] = true
	}

	return result
}

// variantKeys lists a symbol's variant locations. Variant signatures are
// not printed in the dump, so they are not part of its structure.
func variantKeys(variants []Variant) []string {
	var result []string
	for _, variant := range variants {
		result = append(result, fmt.Sprintf("%s:%d", variant.File, variant.Line))
	}

	sort.Strings(result)
	return result
}

func unique(sorted []string) []string {
	if len(sorted) < 2 {
		return sorted
	}

	result := sorted[:1]
	for _, value := range sorted[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}

	return result
}
