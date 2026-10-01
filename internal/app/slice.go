package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const SliceSchema = "treaty/slice/v1"

var ErrUnknownTarget = errors.New("unknown symbol, module or file")

// Neighbor is one direct use or caller in a slice, with where it is
// declared, so it can be read without opening whole files.
type Neighbor struct {
	Symbol    string `json:"symbol"`
	Relation  string `json:"relation"`
	Signature string `json:"signature,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
}

// Slice is the minimum context an agent needs for one symbol, file or
// module. A file slice lists every declaration in the file; a module slice
// lists its contracts and its files, the narrower targets to slice next.
type Slice struct {
	Schema     string            `json:"schema"`
	TaskScope  SliceScope        `json:"task_scope"`
	Contract   *SliceContract    `json:"contract,omitempty"`
	Contracts  []SliceSymbol     `json:"contracts,omitempty"`
	Symbols    []SliceSymbol     `json:"symbols,omitempty"`
	Files      []SliceFile       `json:"files,omitempty"`
	MayDepend  []string          `json:"may_depend_on_layers"`
	Neighbors  []Neighbor        `json:"neighbors,omitempty"`
	Dependents []string          `json:"dependents,omitempty"`
	Violations []rules.Violation `json:"violations,omitempty"`
	Excluded   SliceExcluded     `json:"excluded"`
}

// SliceContract is the target symbol's signature and change.
type SliceContract struct {
	Signature string `json:"signature"`
	Change    string `json:"change,omitempty"`
}

// SliceExcluded counts what the slice left out.
type SliceExcluded struct {
	Modules int `json:"modules"`
	Symbols int `json:"symbols"`
}

// SliceFile is one file of a module slice, with how much it declares.
type SliceFile struct {
	File      string `json:"file"`
	Symbols   int    `json:"symbols"`
	Contracts int    `json:"contracts"`
}

// SliceScope names the slice's target and, for a symbol, the lines it spans.
type SliceScope struct {
	Symbol  string `json:"symbol,omitempty"`
	Module  string `json:"module,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Layer   string `json:"layer"`
	Kind    string `json:"kind,omitempty"`
}

// SliceSymbol is one symbol listed in a file or module slice.
type SliceSymbol struct {
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// Slice builds a context slice for a symbol, file or module in the working
// tree.
//
// Parameters:
//   - target: a symbol id, a module id, or a file as a path relative to the
//     repository root, with or without its language prefix; or a short name
//     that matches exactly one, such as Store.CreateBook or store.go.
//
// Returns:
//   - result: the slice.
//   - err: the tree could not be read, or the target does not exist.
//
// Errors:
//   - ErrUnknownTarget: nothing matches the target.
//   - ErrAmbiguousTarget: several symbols, modules or files match it.
func (this *Service) Slice(target string) (result Slice, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return Slice{}, err
	}

	return buildSlice(analysis, target)
}

func buildSlice(analysis *analysis, target string) (Slice, error) {
	target, err := analysis.resolve(target)
	if err != nil {
		return Slice{}, err
	}

	g := analysis.head
	result := Slice{Schema: SliceSchema}
	included := map[string]bool{}
	modules := map[string]bool{}
	if symbol := g.Symbol(target); symbol != nil {
		module := g.Module(symbol.Module)
		result.TaskScope = SliceScope{Symbol: symbol.ID, File: symbol.File, Line: symbol.Line, EndLine: symbol.EndLine, Layer: module.Layer, Kind: symbol.Kind}
		result.Contract = &SliceContract{Signature: symbol.Signature, Change: changeOf(analysis, symbol.ID)}
		result.MayDepend = analysis.config.Architecture.MayUse(placement(module))
		included[symbol.ID], modules[module.ID] = true, true
		for _, edge := range g.Edges {
			var other, relation string
			switch {
			case edge.From == symbol.ID:
				other, relation = edge.To, "uses"
			case edge.To == symbol.ID:
				other, relation = edge.From, "called_by"
			default:
				continue
			}

			if included[other] {
				continue
			}

			neighbor := g.Symbol(other)
			included[other], modules[neighbor.Module] = true, true
			result.Neighbors = append(result.Neighbors, neighborOf(neighbor, relation, symbol.Module))
		}

		sort.Slice(result.Neighbors, func(i, j int) bool { return result.Neighbors[i].Symbol < result.Neighbors[j].Symbol })
	} else if module, file := fileTarget(g, target); module != nil {
		result.TaskScope = SliceScope{Module: module.ID, File: file, Layer: module.Layer}
		result.MayDepend = analysis.config.Architecture.MayUse(placement(module))
		modules[module.ID] = true
		for _, symbol := range g.SymbolsIn(module.ID) {
			if symbol.File == file {
				included[symbol.ID] = true
				result.Symbols = append(result.Symbols, sliceSymbol(symbol))
			}
		}

		// A neighbor is a symbol in another file that this file's symbols
		// use or are used by; uses come first when a symbol is both.
		relations := map[string]string{}
		for _, edge := range g.Edges {
			switch {
			case included[edge.From] && !included[edge.To] && g.Symbol(edge.To).File != file:
				relations[edge.To] = "uses"
			case included[edge.To] && !included[edge.From] && g.Symbol(edge.From).File != file && relations[edge.From] == "":
				relations[edge.From] = "called_by"
			}
		}

		for id, relation := range relations {
			neighbor := g.Symbol(id)
			modules[neighbor.Module] = true
			result.Neighbors = append(result.Neighbors, neighborOf(neighbor, relation, module.ID))
		}

		sort.Slice(result.Neighbors, func(i, j int) bool { return result.Neighbors[i].Symbol < result.Neighbors[j].Symbol })
		for _, violation := range analysis.violations {
			if violation.From == module.ID && violationTouches(violation, file) {
				result.Violations = append(result.Violations, violation)
			}
		}

		for id := range relations {
			included[id] = true
		}
	} else if module := g.Module(target); module != nil {
		result.TaskScope = SliceScope{Module: module.ID, Layer: module.Layer}
		result.MayDepend = analysis.config.Architecture.MayUse(placement(module))
		modules[module.ID] = true
		files := map[string]*SliceFile{}
		for _, file := range module.Files {
			files[file] = &SliceFile{File: file}
		}

		for _, symbol := range g.SymbolsIn(module.ID) {
			if entry := files[symbol.File]; entry != nil {
				entry.Symbols++
				if symbol.Contract {
					entry.Contracts++
				}
			}

			if symbol.Contract {
				included[symbol.ID] = true
				result.Contracts = append(result.Contracts, sliceSymbol(symbol))
			}
		}

		for _, file := range module.Files {
			result.Files = append(result.Files, *files[file])
		}

		for _, edge := range g.ModuleEdges() {
			if edge.To == module.ID {
				result.Dependents = append(result.Dependents, edge.From)
			}
		}

		for _, violation := range analysis.violations {
			if violation.From == module.ID {
				result.Violations = append(result.Violations, violation)
			}
		}
	} else {
		return Slice{}, fmt.Errorf("%w: %s", ErrUnknownTarget, target)
	}

	result.Excluded = SliceExcluded{Modules: len(g.Modules) - len(modules), Symbols: len(g.Symbols) - len(included)}
	return result, nil
}

// fileTarget finds the module holding a file named by target, a path
// relative to the repository root with or without its language prefix.
func fileTarget(g *graph.Graph, target string) (module *graph.Module, file string) {
	for _, candidate := range g.Modules {
		for _, each := range candidate.Files {
			if target == each || target == candidate.Language+":"+each {
				return candidate, each
			}
		}
	}

	return nil, ""
}

// neighborOf describes a neighbor. Its signature is shown when it is a
// contract or lives in the target's own module.
func neighborOf(symbol *graph.Symbol, relation, module string) Neighbor {
	result := Neighbor{Symbol: symbol.ID, Relation: relation, File: symbol.File, Line: symbol.Line}
	if symbol.Module == module || symbol.Contract {
		result.Signature = symbol.Signature
	}

	return result
}

func sliceSymbol(symbol *graph.Symbol) SliceSymbol {
	return SliceSymbol{Symbol: symbol.ID, Kind: symbol.Kind, Signature: symbol.Signature, File: symbol.File, Line: symbol.Line, EndLine: symbol.EndLine}
}

// violationTouches reports whether a violation has a reference or import in
// the file.
func violationTouches(violation rules.Violation, file string) bool {
	for _, ref := range violation.References {
		if ref.File == file {
			return true
		}
	}

	for _, item := range violation.Imports {
		if item.File == file {
			return true
		}
	}

	return false
}

func changeOf(analysis *analysis, id string) string {
	for _, change := range analysis.changes {
		if change.Symbol == id {
			return change.Kind
		}
	}

	return ""
}

// Text renders the slice compactly, one line per entry, for agents: the
// same content as the JSON form in a fraction of the characters. Symbols in
// the target's own module are named without their module, since short names
// resolve.
//
// Returns:
//   - result: the slice as text.
func (this Slice) Text() string {
	var builder strings.Builder
	scope := this.TaskScope
	module := scope.Module
	switch {
	case scope.Symbol != "":
		module, _ = graph.SplitSymbolID(scope.Symbol)
		fmt.Fprintf(&builder, "slice %s  %s  %s:%d-%d  layer %s\n", scope.Symbol, scope.Kind, scope.File, scope.Line, scope.EndLine, scope.Layer)
	case scope.File != "":
		fmt.Fprintf(&builder, "slice %s  module %s  layer %s\n", scope.File, scope.Module, scope.Layer)
	default:
		fmt.Fprintf(&builder, "slice %s  layer %s\n", scope.Module, scope.Layer)
	}

	if this.Contract != nil {
		fmt.Fprintf(&builder, "contract: %s", this.Contract.Signature)
		if this.Contract.Change != "" {
			fmt.Fprintf(&builder, "  (change: %s)", this.Contract.Change)
		}

		builder.WriteString("\n")
	}

	fmt.Fprintf(&builder, "may depend on: %s\n", strings.Join(this.MayDepend, ", "))
	short := func(id string) string {
		if owner, name := graph.SplitSymbolID(id); owner == module {
			return name
		}

		return id
	}

	if len(this.Symbols) > 0 {
		fmt.Fprintf(&builder, "symbols (%d):\n", len(this.Symbols))
		for _, symbol := range this.Symbols {
			fmt.Fprintf(&builder, "  %d-%d  %s  %s  %s\n", symbol.Line, symbol.EndLine, symbol.Kind, short(symbol.Symbol), symbol.Signature)
		}
	}

	if len(this.Contracts) > 0 {
		fmt.Fprintf(&builder, "contracts (%d):\n", len(this.Contracts))
		for _, symbol := range this.Contracts {
			fmt.Fprintf(&builder, "  %s:%d  %s  %s  %s\n", symbol.File, symbol.Line, symbol.Kind, short(symbol.Symbol), symbol.Signature)
		}
	}

	if len(this.Files) > 0 {
		fmt.Fprintf(&builder, "files (%d):\n", len(this.Files))
		for _, file := range this.Files {
			fmt.Fprintf(&builder, "  %s  %d symbols, %d contracts\n", file.File, file.Symbols, file.Contracts)
		}
	}

	if len(this.Neighbors) > 0 {
		fmt.Fprintf(&builder, "neighbors (%d):\n", len(this.Neighbors))
		for _, neighbor := range this.Neighbors {
			fmt.Fprintf(&builder, "  %-9s  %s  %s:%d", neighbor.Relation, short(neighbor.Symbol), neighbor.File, neighbor.Line)
			if neighbor.Signature != "" {
				fmt.Fprintf(&builder, "  %s", neighbor.Signature)
			}

			builder.WriteString("\n")
		}
	}

	if len(this.Dependents) > 0 {
		fmt.Fprintf(&builder, "dependents: %s\n", strings.Join(this.Dependents, ", "))
	}

	for _, violation := range this.Violations {
		file, line, first := violation.First()
		fmt.Fprintf(&builder, "violation: %s → %s: %s; first %s at %s:%d\n", violation.From, violation.To, violation.Rule, first, file, line)
	}

	fmt.Fprintf(&builder, "excluded: %d modules, %d symbols\n", this.Excluded.Modules, this.Excluded.Symbols)
	return builder.String()
}
