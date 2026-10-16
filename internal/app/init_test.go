package app

import (
	"errors"
	"maps"
	"reflect"
	"slices"
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

func TestProposeVertical(t *testing.T) {
	root, proposal := proposeSections(shapeGraph(), rules.StyleVertical)
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

	draft := sectionDraft(rules.StyleVertical, root, proposal)
	for _, want := range []string{"  billing: [\"internal/features/billing/**\"]\n  orders: [\"internal/features/orders/**\"]\n", `"internal/features/money/**"`, "fail_on: [breaking_changes, layer_violation, unclassified, ambiguous_boundaries]"} {
		if !strings.Contains(draft, want) {
			t.Errorf("draft should have %q:\n%s", want, draft)
		}
	}

	if strings.Contains(draft, "  money:") {
		t.Errorf("shared code is no boundary:\n%s", draft)
	}

	architecture := architectureFrom(rules.StyleVertical, root, proposal)
	if got := architecture.Resolve("internal/features/orders/store"); got.Boundary != "orders" {
		t.Errorf("orders/store placed %+v", got)
	}
}

func TestProposeModular(t *testing.T) {
	root, proposal := proposeSections(shapeGraph(), rules.StyleModular)
	if root != "internal/features" {
		t.Fatalf("root %q, proposal %v", root, proposal)
	}

	if proposal["internal/features/money"] != groupSection {
		t.Errorf("modular boundaries are never shared: money proposed as %q", proposal["internal/features/money"])
	}

	draft := sectionDraft(rules.StyleModular, root, proposal)
	for _, want := range []string{"  money: [\"internal/features/money/**\"]", "dependency_cycles"} {
		if !strings.Contains(draft, want) {
			t.Errorf("draft should have %q:\n%s", want, draft)
		}
	}

	architecture := architectureFrom(rules.StyleModular, root, proposal)
	if got := architecture.Resolve("internal/features/orders"); got.Boundary != "orders" || !got.Public {
		t.Errorf("the top of a boundary is public by default: %+v", got)
	}

	if got := architecture.Resolve("internal/features/orders/store"); got.Boundary != "orders" || got.Public {
		t.Errorf("orders/store is internal: %+v", got)
	}
}

// shapeGraph is a small program: cmd/app wires two features that share
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

func TestProposeNone(t *testing.T) {
	architecture, text := propose(shapeGraph(), rules.StyleNone)
	if architecture.Style != rules.StyleNone || architecture.Validate() != nil {
		t.Fatalf("none proposes no architecture: %+v", architecture)
	}

	if !strings.Contains(text, "architecture: none\n") || strings.Contains(text, "layers:") {
		t.Fatalf("draft:\n%s", text)
	}

	for _, style := range []string{rules.StyleHexagonal, rules.StyleClean, rules.StyleLayered} {
		if _, text := propose(shapeGraph(), style); !strings.Contains(text, "architecture: "+style+"\n") {
			t.Errorf("a %s draft names its architecture, since a missing key now means none:\n%s", style, text)
		}
	}
}

func TestAdaptersDirectoryLeavesTheSideToTheCode(t *testing.T) {
	g := graph.New()
	for _, path := range []string{"cmd/server", "internal/adapters/httpapi", "internal/adapters/files", "internal/app", "internal/domain"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Signature: "type X struct"})
	}

	g.AddSymbol(&graph.Symbol{ID: "go:internal/app:Store", Module: "go:internal/app", Name: "Store", Kind: graph.KindInterface, Signature: "type Store interface"})
	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	// The HTTP adapter calls the application; the file adapter implements
	// one of its ports. Neither name says which side it is on.
	uses("cmd/server", "internal/adapters/httpapi")
	uses("cmd/server", "internal/adapters/files")
	uses("internal/adapters/httpapi", "internal/app")
	uses("internal/adapters/files", "internal/app")
	uses("internal/app", "internal/domain")
	g.AddEdge(graph.Edge{From: "go:internal/adapters/files:X", To: "go:internal/app:Store", Kind: graph.EdgeImplements})
	g.Normalize()

	got := proposeLayers(g, rules.StyleHexagonal)
	if got["internal/adapters/httpapi"] != graph.SideDriving || got["internal/adapters/files"] != graph.SideDriven {
		t.Fatalf("adapters under adapters/ take their side from the code: %v", got)
	}
}

// starGraph builds a graph from module paths and the module dependencies
// between them, marking cmd/ paths as programs.
func starGraph(paths []string, uses [][2]string) *graph.Graph {
	g := graph.New()
	for _, path := range paths {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path, Entry: strings.HasPrefix(path, "cmd/")})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Signature: "type X struct"})
	}

	for _, use := range uses {
		g.AddEdge(graph.Edge{From: "go:" + use[0] + ":X", To: "go:" + use[1] + ":X", Kind: graph.EdgeTypeUse})
	}

	g.Normalize()
	return g
}

func TestProposeStarsReproducesTheReorganization(t *testing.T) {
	spokes := []string{"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web"}
	blocks := []string{"internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www"}
	gate := []string{"internal/gate/presence", "internal/gate/tagger"}
	paths := []string{".", "cmd/guardprobe", "internal/factory", "internal/core", "internal/core/trust", "internal/blocks", "internal/blocks/core", "internal/gate", "internal/gate/core"}
	paths = append(append(append(paths, spokes...), blocks...), gate...)
	uses := [][2]string{
		{"cmd/guardprobe", "."}, {".", "internal/factory"}, {".", "internal/core"},
		{"internal/core", "internal/core/trust"},
		{"internal/blocks", "internal/blocks/core"}, {"internal/blocks/core", "internal/core"},
		{"internal/gate", "internal/gate/core"}, {"internal/gate/core", "internal/core"},
	}

	for _, spoke := range append(append(append([]string{"internal/blocks", "internal/gate"}, spokes...), blocks...), gate...) {
		uses = append(uses, [2]string{"internal/factory", spoke}, [2]string{spoke, "internal/core"})
	}

	for _, spoke := range blocks {
		uses = append(uses, [2]string{spoke, "internal/blocks/core"})
	}

	for _, spoke := range gate {
		uses = append(uses, [2]string{spoke, "internal/gate/core"})
	}

	g := starGraph(paths, uses)
	architecture, text := propose(g, rules.StyleSnowflake)
	want := `version: 1
architecture: snowflake
composition: [".", "cmd/**", "internal/factory/**"]
stars: # each star's directory -> the globs of its core; a star's own package is in its core
  internal: ["internal/core/**"]
  internal/blocks: ["internal/blocks/core/**"]
  internal/gate: ["internal/gate/core/**"]
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
`
	if !strings.HasSuffix(text, want) {
		t.Fatalf("the draft must end with\n%s\ngot\n%s", want, text)
	}

	for _, line := range []string{
		"#   internal  core internal, internal/core/**; leaves internal/ai, internal/anthropic, internal/plan, internal/tensors, internal/tracer, internal/web; branches internal/blocks, internal/gate\n",
		"#   internal/blocks  branch of internal; core internal/blocks, internal/blocks/core/**; leaves internal/blocks/cleanse, internal/blocks/spotlight, internal/blocks/summarize, internal/blocks/www\n",
		"# treaty check lists no dependency that breaks these rules.\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the header lacks %q:\n%s", line, text)
		}
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	architecture.Assign(g)
	if violations := architecture.Violations(g); len(violations) > 0 {
		t.Fatalf("the reorganization has no violations: %+v", violations)
	}

	if adopted, err := proposal(g, rules.StyleSnowflake); err != nil || adopted != text {
		t.Fatalf("adopt writes the same text: %v", err)
	}
}

func TestProposeStarsOnCodeNotYetASnowflake(t *testing.T) {
	g := starGraph(
		[]string{"cmd/guard", "internal/trust", "internal/plan", "internal/web", "internal/blocks", "internal/blocks/contracts", "internal/blocks/www", "scripts/x"},
		[][2]string{
			{"cmd/guard", "internal/plan"}, {"internal/plan", "internal/trust"}, {"internal/web", "internal/trust"},
			{"internal/plan", "internal/web"}, {"internal/plan", "internal/blocks/contracts"},
			{"internal/blocks", "internal/blocks/contracts"}, {"internal/blocks/www", "internal/blocks/contracts"},
			{"internal/blocks/contracts", "internal/trust"}, {"internal/plan", "internal/blocks"}, {"internal/plan", "internal/blocks/www"},
		},
	)

	architecture, text := propose(g, rules.StyleSnowflake)
	want := []rules.Star{{Dir: "internal", Core: []string{"internal/trust/**"}}, {Dir: "internal/blocks", Core: []string{"internal/blocks/contracts/**"}}}
	if !reflect.DeepEqual(architecture.Stars, want) || !slices.Equal(architecture.Composition, []string{"cmd/**"}) {
		t.Fatalf("stars %+v, composition %v", architecture.Stars, architecture.Composition)
	}

	for _, line := range []string{
		"# treaty check lists 4 dependencies that break these rules: 4 between sibling spokes.\n",
		"# Unclassified, outside the root star and not composition: scripts/x\n",
		"  internal/blocks: [\"internal/blocks/contracts/**\"]\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the draft lacks %q:\n%s", line, text)
		}
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	// A star with no child its siblings could share says so, and names the
	// children that two or more siblings use.
	g = starGraph(
		[]string{"cmd/x", "lib/a", "lib/b", "lib/c"},
		[][2]string{{"cmd/x", "lib/c"}, {"lib/a", "lib/b"}, {"lib/b", "lib/a"}, {"lib/c", "lib/a"}},
	)

	architecture, text = propose(g, rules.StyleSnowflake)
	if len(architecture.Stars) != 1 || len(architecture.Stars[0].Core) != 0 {
		t.Fatalf("stars %+v", architecture.Stars)
	}

	for _, line := range []string{
		"# No core in lib yet: every dependency between its children is sideways. Create lib/core and move the contracts there.\n",
		"# Candidates for lib/core: lib/a.\n",
		"  lib: []\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the draft lacks %q:\n%s", line, text)
		}
	}
}

func TestProposeStarsRefusesProgramsAlone(t *testing.T) {
	g := starGraph([]string{"cmd/a", "cmd/b"}, nil)
	if _, err := proposal(g, rules.StyleSnowflake); !errors.Is(err, rules.ErrArchitecture) || !strings.Contains(err.Error(), "found no packages outside composition to make a star of, so snowflake does not fit this code") {
		t.Fatalf("programs alone: %v", err)
	}
}
