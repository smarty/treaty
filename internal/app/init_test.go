package app

import (
	"maps"
	"strings"
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

	got := proposeLayers(g, rules.StyleHexagonal)
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

	architecture := rules.Architecture{Style: rules.StyleHexagonal, Layers: []rules.Layer{
		{Name: graph.LayerDomain}, {Name: graph.LayerApplication},
		{Name: graph.LayerAdapter, Side: graph.SideDriving}, {Name: graph.LayerAdapter, Side: graph.SideDriven},
	}}

	for _, entry := range compressGlobs(got) {
		switch entry.group {
		case graph.LayerComposition:
			architecture.Composition = append(architecture.Composition, entry.pattern)
		case graph.LayerDomain:
			architecture.Layers[0].Globs = append(architecture.Layers[0].Globs, entry.pattern)
		case graph.LayerApplication:
			architecture.Layers[1].Globs = append(architecture.Layers[1].Globs, entry.pattern)
		case graph.SideDriving:
			architecture.Layers[2].Globs = append(architecture.Layers[2].Globs, entry.pattern)
		case graph.SideDriven:
			architecture.Layers[3].Globs = append(architecture.Layers[3].Globs, entry.pattern)
		}
	}

	architecture.Assign(g)
	if violations := architecture.Violations(g); len(violations) > 0 {
		t.Fatalf("the proposal must have no violations: %+v", violations)
	}
}

func TestProposeRingsAndBandsHaveNoViolations(t *testing.T) {
	for _, style := range []string{rules.StyleClean, rules.StyleLayered} {
		g := shapeGraph()
		proposal := proposeLayers(g, style)
		architecture := rules.Architecture{Style: style}
		names := outsideIn(style)
		for i := len(names) - 1; i >= 0; i-- {
			architecture.Layers = append(architecture.Layers, rules.Layer{Name: names[i]})
		}

		for _, entry := range compressGlobs(proposal) {
			if entry.group == graph.LayerComposition {
				architecture.Composition = append(architecture.Composition, entry.pattern)
				continue
			}

			for i := range architecture.Layers {
				if architecture.Layers[i].Name == entry.group {
					architecture.Layers[i].Globs = append(architecture.Layers[i].Globs, entry.pattern)
				}
			}
		}

		if err := architecture.Validate(); err != nil {
			t.Fatalf("%s: %v", style, err)
		}

		architecture.Assign(g)
		if violations := architecture.Violations(g); len(violations) > 0 {
			t.Errorf("%s: the proposal must have no violations: %+v", style, violations)
		}

		if got := proposal["cmd/app"]; got != graph.LayerComposition {
			t.Errorf("%s: cmd/app proposed as %q", style, got)
		}
	}
}

func TestProposeSlices(t *testing.T) {
	root, proposal := proposeSections(shapeGraph(), rules.StyleSlices)
	if root != "internal/features" {
		t.Fatalf("root %q, proposal %v", root, proposal)
	}

	want := map[string]string{
		"cmd/app":                         graph.LayerComposition,
		"internal/features/orders":        groupSection,
		"internal/features/orders/store":  groupSection,
		"internal/features/billing":       groupSection,
		"internal/features/money":         graph.LayerShared,
		"internal/platform/log":           graph.LayerShared,
		"internal/features/billing/store": groupSection,
	}

	for modulePath, group := range want {
		if proposal[modulePath] != group {
			t.Errorf("%s proposed as %q, want %q", modulePath, proposal[modulePath], group)
		}
	}

	draft := sectionDraft(rules.StyleSlices, root, proposal)
	if !strings.Contains(draft, `slices: ["internal/features/*"]`) || !strings.Contains(draft, `"internal/features/money/**"`) {
		t.Errorf("draft:\n%s", draft)
	}
}

func TestProposeContexts(t *testing.T) {
	root, proposal := proposeSections(shapeGraph(), rules.StyleModular)
	if root != "internal/features" {
		t.Fatalf("root %q, proposal %v", root, proposal)
	}

	if proposal["internal/features/money"] != groupSection {
		t.Errorf("contexts are never shared: money proposed as %q", proposal["internal/features/money"])
	}

	if draft := sectionDraft(rules.StyleModular, root, proposal); !strings.Contains(draft, `contexts: ["internal/features/*"]`) || !strings.Contains(draft, `public: ["."]`) {
		t.Errorf("draft:\n%s", draft)
	}
}

// shapeGraph is a small program: cmd/app wires two feature slices that share
// money and a logger.
func shapeGraph() *graph.Graph {
	g := graph.New()
	for _, path := range []string{"cmd/app", "internal/features/orders", "internal/features/orders/store", "internal/features/billing", "internal/features/billing/store", "internal/features/money", "internal/platform/log"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Signature: "type X struct"})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	g.Module("go:cmd/app").Entry = true
	uses("cmd/app", "internal/features/orders")
	uses("cmd/app", "internal/features/billing")
	uses("internal/features/orders", "internal/features/orders/store")
	uses("internal/features/billing", "internal/features/billing/store")
	uses("internal/features/orders", "internal/features/money")
	uses("internal/features/billing", "internal/features/money")
	uses("internal/features/orders/store", "internal/platform/log")
	uses("internal/features/billing/store", "internal/platform/log")
	g.Normalize()
	return g
}
