package app

import (
	"maps"
	"testing"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

func TestProposeLayersFromShape(t *testing.T) {
	g := graph.New()
	for _, path := range []string{"tool", "store", "web", "engine", "model", "lonely", "internal/db", "cmd/probe", "clock"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Signature: "type X struct"})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":Port", Module: "go:" + path, Name: "Port", Kind: graph.KindInterface, Signature: "type Port interface"})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	// tool wires store, web and internal/db; web drives engine; store
	// implements an engine port; engine and store use model.
	uses("tool", "store")
	uses("tool", "web")
	uses("tool", "internal/db")
	uses("web", "engine")
	uses("store", "model")
	uses("engine", "model")
	uses("internal/db", "model")
	g.AddEdge(graph.Edge{From: "go:store:X", To: "go:engine:Port", Kind: graph.EdgeImplements})

	// A program that imports nothing in the repository is still
	// composition, and a leaf used only by an adapter is still domain.
	g.Module("go:cmd/probe").Entry = true
	uses("web", "clock")
	g.Normalize()

	got := proposeLayers(g)
	want := map[string]string{
		"tool":        "composition",
		"store":       "driven",
		"web":         "driving",
		"engine":      "application",
		"model":       "domain",
		"lonely":      "",
		"internal/db": "driven",
		"cmd/probe":   "composition",
		"clock":       "domain",
	}

	if !maps.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}

	layers := rules.Layers{}
	for _, entry := range compressGlobs(got) {
		switch entry.group {
		case "composition":
			layers.Composition = append(layers.Composition, entry.pattern)
		case "domain":
			layers.Domain = append(layers.Domain, entry.pattern)
		case "application":
			layers.Application = append(layers.Application, entry.pattern)
		case "driving":
			layers.Driving = append(layers.Driving, entry.pattern)
		case "driven":
			layers.Driven = append(layers.Driven, entry.pattern)
		}
	}

	layers.Assign(g)
	if violations := rules.Violations(g); len(violations) > 0 {
		t.Fatalf("the proposal must have no violations: %+v", violations)
	}
}
