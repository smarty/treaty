package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/graph"
)

// targetGraph holds C# member names with parameter lists and generic
// markers, and one Go method.
func targetGraph() *graph.Graph {
	g := graph.New()
	g.AddModule(&graph.Module{ID: "cs:src", Language: "cs", Path: "src"})
	g.AddModule(&graph.Module{ID: "go:internal/graph", Language: "go", Path: "internal/graph"})
	for _, each := range []struct{ name, parent string }{
		{"Order.Add(Line,int)", "Order"},
		{"Parser.Parse(string)", "Parser"},
		{"Parser.Parse(string,int)", "Parser"},
		{"Money.op_Implicit(int)Money", "Money"},
		{"Order.Map<>(Func<M0>)", "Order"},
		{"Result<>.Get(int)", "Result<>"},
	} {
		g.AddSymbol(&graph.Symbol{ID: graph.SymbolID("cs:src", each.name), Module: "cs:src", Name: each.name, Parent: each.parent, Kind: graph.KindMethod})
	}

	g.AddSymbol(&graph.Symbol{ID: "go:internal/graph:Graph.Order", Module: "go:internal/graph", Name: "Graph.Order", Parent: "Graph", Kind: graph.KindMethod})
	return g
}

func TestShortTargets(t *testing.T) {
	analysis := &analysis{head: targetGraph()}
	for target, want := range map[string]string{
		"Order.Add":           "cs:src:Order.Add(Line,int)",
		"Add":                 "cs:src:Order.Add(Line,int)",
		"src:Order.Add":       "cs:src:Order.Add(Line,int)",
		"Money.op_Implicit":   "cs:src:Money.op_Implicit(int)Money",
		"Order.Map":           "cs:src:Order.Map<>(Func<M0>)",
		"Map":                 "cs:src:Order.Map<>(Func<M0>)",
		"Result.Get":          "cs:src:Result<>.Get(int)",
		"Order.Add(Line,int)": "cs:src:Order.Add(Line,int)",
		"Graph.Order":         "go:internal/graph:Graph.Order",
	} {
		got, err := analysis.resolve(target)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", target, got, err, want)
		}
	}

	_, err := analysis.resolve("Parser.Parse")
	if !errors.Is(err, ErrAmbiguousTarget) || strings.Count(err.Error(), "cs:src:Parser.Parse(string)") != 1 || strings.Count(err.Error(), "cs:src:Parser.Parse(string,int)") != 1 {
		t.Errorf("Parser.Parse: %v", err)
	}
}
