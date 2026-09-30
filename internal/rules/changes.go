package rules

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	ChangeAdded          = "added"
	ChangeBreaking       = "breaking"
	ChangeContract       = "contract"
	ChangeImplementation = "implementation"
	ChangeMoved          = "moved"
	ChangeRemoved        = "removed"
)

// Change is the change kind of one symbol between a base and a head graph.
type Change struct {
	Symbol string `json:"symbol"`
	Module string `json:"module"`
	Kind   string `json:"change"`
	Before string `json:"before_signature,omitempty"`
	After  string `json:"signature,omitempty"`

	// From is the symbol's id in the base graph when it moved or was renamed.
	From string `json:"from,omitempty"`

	// FieldsRemoved and FieldsAdded list the field lines that differ.
	FieldsRemoved []string `json:"fields_removed,omitempty"`
	FieldsAdded   []string `json:"fields_added,omitempty"`

	// Absorbed marks a breaking change or removal that only this repository
	// can see: the module is private and every dependent changed with it. It
	// is still breaking.
	Absorbed bool `json:"absorbed,omitempty"`
}

// Compatibility decides whether a changed signature is compatible. Each
// language supplies its own rules.
type Compatibility func(before, after *graph.Symbol) bool

// Classify compares two graphs and gives every changed symbol one kind.
//
// Notes:
//   - A method added to an interface that already existed makes the
//     interface breaking, since existing implementers no longer satisfy it.
//   - Removing an internal symbol is an implementation change of nothing and
//     is not listed.
//   - A removed contract symbol that pairs with exactly one added symbol is
//     one moved change rather than a removal and an addition. They pair when
//     the name is kept and the module changed, or the module is kept and
//     only the name changed in the signature. Members of a moved type are
//     folded into the type's change.
//   - Nothing can import an entry module, so its removed symbols are not
//     listed and its breaking changes are implementation changes. Moves out
//     of it are still paired.
//   - A breaking change or removal in a private module is absorbed when
//     every dependent in head changed too.
//
// Parameters:
//   - base: the graph before the change.
//   - head: the graph after the change.
//   - compatible: the language rules for changed signatures.
//
// Returns:
//   - result: changes sorted by symbol id; unchanged symbols are not listed.
func Classify(base, head *graph.Graph, compatible Compatibility) []Change {
	byID := map[string]*Change{}
	for _, after := range head.Symbols {
		before := base.Symbol(after.ID)
		switch {
		case before == nil:
			byID[after.ID] = &Change{Kind: ChangeAdded}
			if parent := base.Symbol(graph.SymbolID(after.Module, after.Parent)); after.Parent != "" && parent != nil && parent.Kind == graph.KindInterface && after.Contract {
				byID[parent.ID] = &Change{Kind: ChangeBreaking}
			}
		case !sameShape(before, after):
			kind := ChangeBreaking
			if before.Contract && after.Contract && compatible(before, after) {
				kind = ChangeContract
			}

			if !before.Contract && !after.Contract {
				kind = ChangeImplementation
			}

			if byID[after.ID] == nil {
				byID[after.ID] = &Change{Kind: kind}
			}
		case before.Hash != after.Hash:
			if byID[after.ID] == nil {
				byID[after.ID] = &Change{Kind: ChangeImplementation}
			}
		}
	}

	for _, before := range base.Symbols {
		if head.Symbol(before.ID) == nil && before.Contract {
			byID[before.ID] = &Change{Kind: ChangeRemoved}
		}
	}

	moves := pairMoves(base, head, byID)
	reach(base, head, byID, moves)
	var result []Change
	for id, change := range byID {
		change.Symbol = id
		if symbol := head.Symbol(id); symbol != nil {
			change.Module, change.After = symbol.Module, symbol.Signature
		}

		if symbol := base.Symbol(id); symbol != nil {
			change.Module, change.Before = symbol.Module, symbol.Signature
			if change.Before == change.After {
				change.Before = ""
				change.FieldsRemoved, change.FieldsAdded = fieldDiff(symbol, head.Symbol(id))
			}
		}

		result = append(result, *change)
	}

	for to, from := range moves {
		before, after := base.Symbol(from), head.Symbol(to)
		change := Change{Symbol: to, Module: after.Module, Kind: ChangeMoved, From: from, After: after.Signature}
		if before.Signature != after.Signature {
			change.Before = before.Signature
		}

		change.FieldsRemoved, change.FieldsAdded = fieldDiff(before, after)
		result = append(result, change)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Symbol < result[j].Symbol })
	return result
}

// DefaultCompatible treats a change as compatible when the signature is
// unchanged and the contract fields only grew.
//
// Parameters:
//   - before: the symbol in the base graph.
//   - after: the symbol in the head graph.
//
// Returns:
//   - result: true when existing callers keep working.
func DefaultCompatible(before, after *graph.Symbol) bool {
	if before.Signature != after.Signature || before.Pointer != after.Pointer {
		return false
	}

	present := map[string]bool{}
	for _, field := range after.Fields {
		present[field.Text] = true
	}

	for _, field := range before.Fields {
		if field.Contract && !present[field.Text] {
			return false
		}
	}

	return true
}

// fieldDiff lists the field lines only in before and only in after.
func fieldDiff(before, after *graph.Symbol) (removed, added []string) {
	if before == nil || after == nil {
		return nil, nil
	}

	inBefore, inAfter := map[string]bool{}, map[string]bool{}
	for _, field := range before.Fields {
		inBefore[field.Text] = true
	}

	for _, field := range after.Fields {
		inAfter[field.Text] = true
		if !inBefore[field.Text] {
			added = append(added, field.Text)
		}
	}

	for _, field := range before.Fields {
		if !inAfter[field.Text] {
			removed = append(removed, field.Text)
		}
	}

	return removed, added
}

// pairMoves finds removed symbols that reappear as added ones, drops both
// from changes, and returns the pairs as head id → base id.
func pairMoves(base, head *graph.Graph, changes map[string]*Change) map[string]string {
	type key struct{ kind, a, b string }
	candidates := func(g *graph.Graph, kind string, keyOf func(*graph.Symbol) key) map[key][]string {
		result := map[key][]string{}
		for id, change := range changes {
			if change.Kind != kind {
				continue
			}

			if symbol := g.Symbol(id); symbol != nil {
				k := keyOf(symbol)
				result[k] = append(result[k], id)
			}
		}

		return result
	}

	result := map[string]string{}
	pair := func(keyOf func(*graph.Symbol) key) {
		removed := candidates(base, ChangeRemoved, keyOf)
		added := candidates(head, ChangeAdded, keyOf)
		for k, from := range removed {
			if to := added[k]; len(from) == 1 && len(to) == 1 {
				result[to[0]] = from[0]
				delete(changes, from[0])
				delete(changes, to[0])
			}
		}
	}

	// The same name in a different module.
	pair(func(s *graph.Symbol) key { return key{s.Kind, s.Name, ""} })

	// The same module and shape under a different name.
	pair(func(s *graph.Symbol) key {
		short := s.Name[strings.LastIndex(s.Name, ".")+1:]
		return key{s.Kind, s.Module + " " + s.Parent, strings.Replace(s.Signature, short, "\x00", 1) + fmt.Sprint(s.Fields)}
	})

	// A member of a moved type moved with it, even when the type was renamed.
	for to, from := range result {
		after, before := head.Symbol(to), base.Symbol(from)
		if after.Kind != graph.KindType && after.Kind != graph.KindInterface {
			continue
		}

		for id, change := range changes {
			member := base.Symbol(id)
			if change.Kind != ChangeRemoved || member == nil || member.Module != before.Module || member.Parent != before.Name {
				continue
			}

			moved := graph.SymbolID(after.Module, after.Name+strings.TrimPrefix(member.Name, before.Name))
			if added, ok := changes[moved]; ok && added.Kind == ChangeAdded && head.Symbol(moved).Signature == member.Signature {
				delete(changes, id)
				delete(changes, moved)
			}
		}
	}

	for to, from := range result {
		after, before := head.Symbol(to), base.Symbol(from)
		if after.Parent == "" {
			continue
		}

		parent := graph.SymbolID(after.Module, after.Parent)
		if moved, ok := result[parent]; ok && moved == graph.SymbolID(before.Module, before.Parent) && after.Signature == before.Signature {
			delete(result, to)
		}
	}

	return result
}

// reach applies what can depend on a changed symbol: nothing, for an entry
// module, and only this repository, for a private one.
func reach(base, head *graph.Graph, changes map[string]*Change, moves map[string]string) {
	changed := map[string]bool{}
	for id := range changes {
		changed[id] = true
	}

	for to := range moves {
		changed[to] = true
	}

	callers := map[string][]string{}
	for _, edge := range head.Edges {
		callers[edge.To] = append(callers[edge.To], edge.From)
	}

	for id, change := range changes {
		if change.Kind != ChangeBreaking && change.Kind != ChangeRemoved {
			continue
		}

		g := head
		symbol := head.Symbol(id)
		if symbol == nil {
			g, symbol = base, base.Symbol(id)
		}

		module := g.Module(symbol.Module)
		switch {
		case module.Entry && change.Kind == ChangeRemoved:
			delete(changes, id)
		case module.Entry:
			change.Kind = ChangeImplementation
		case module.Private && !slices.ContainsFunc(callers[id], func(caller string) bool { return !changed[caller] }):
			change.Absorbed = true
		}
	}
}

func sameShape(before, after *graph.Symbol) bool {
	if before.Signature != after.Signature || before.Pointer != after.Pointer || before.Contract != after.Contract {
		return false
	}

	if len(before.Fields) != len(after.Fields) {
		return false
	}

	for i := range before.Fields {
		if before.Fields[i] != after.Fields[i] {
			return false
		}
	}

	return true
}
