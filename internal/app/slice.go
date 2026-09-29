package app

import (
	"errors"
	"fmt"
	"sort"

	"github.com/smarty/treaty/internal/rules"
)

const SliceSchema = "treaty/slice/v1"

var ErrUnknownTarget = errors.New("unknown symbol or module")

// Neighbor is one direct use or caller in a slice.
type Neighbor struct {
	Symbol    string `json:"symbol"`
	Relation  string `json:"relation"`
	Signature string `json:"signature,omitempty"`
}

// Slice is the minimum context an agent needs for one symbol or module.
type Slice struct {
	Schema     string            `json:"schema"`
	TaskScope  SliceScope        `json:"task_scope"`
	Contract   *SliceContract    `json:"contract,omitempty"`
	Contracts  []SliceSymbol     `json:"contracts,omitempty"`
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

// SliceScope names the slice's target.
type SliceScope struct {
	Symbol string `json:"symbol,omitempty"`
	Module string `json:"module,omitempty"`
	File   string `json:"file,omitempty"`
	Layer  string `json:"layer"`
	Kind   string `json:"kind,omitempty"`
}

// SliceSymbol is one contract listed in a module slice.
type SliceSymbol struct {
	Symbol    string `json:"symbol"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
}

// Slice builds a context slice for a symbol or module in the working tree.
//
// Parameters:
//   - target: a symbol id or module id.
//
// Returns:
//   - result: the slice.
//   - err: the tree could not be read, or the target does not exist.
//
// Errors:
//   - ErrUnknownTarget: no symbol or module has that id.
func (this *Service) Slice(target string) (result Slice, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return Slice{}, err
	}

	return buildSlice(analysis, target)
}

func buildSlice(analysis *analysis, target string) (Slice, error) {
	g := analysis.head
	result := Slice{Schema: SliceSchema}
	included := map[string]bool{}
	modules := map[string]bool{}
	if symbol := g.Symbol(target); symbol != nil {
		module := g.Module(symbol.Module)
		result.TaskScope = SliceScope{Symbol: symbol.ID, File: symbol.File, Layer: module.Layer, Kind: symbol.Kind}
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
			entry := Neighbor{Symbol: other, Relation: relation}
			if neighbor.Module == symbol.Module || neighbor.Contract {
				entry.Signature = neighbor.Signature
			}

			result.Neighbors = append(result.Neighbors, entry)
		}

		sort.Slice(result.Neighbors, func(i, j int) bool { return result.Neighbors[i].Symbol < result.Neighbors[j].Symbol })
	} else if module := g.Module(target); module != nil {
		result.TaskScope = SliceScope{Module: module.ID, Layer: module.Layer}
		result.MayDepend = analysis.config.Architecture.MayUse(placement(module))
		modules[module.ID] = true
		for _, symbol := range g.SymbolsIn(module.ID) {
			if symbol.Contract {
				included[symbol.ID] = true
				result.Contracts = append(result.Contracts, SliceSymbol{Symbol: symbol.ID, Kind: symbol.Kind, Signature: symbol.Signature})
			}
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

func changeOf(analysis *analysis, id string) string {
	for _, change := range analysis.changes {
		if change.Symbol == id {
			return change.Kind
		}
	}

	return ""
}
