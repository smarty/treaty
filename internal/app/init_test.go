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

// guardGraph is agentportal-guard's import graph: its target tree under the
// snowflake's second version, or, before its move, its tree as it is.
func guardGraph(moved bool) *graph.Graph {
	leaves := []string{"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web"}
	blocks := []string{"internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www"}
	gate := []string{"internal/gate/presence", "internal/gate/tagger"}
	if !moved {
		paths := append([]string{".", "cmd/guardprobe", "internal/core", "internal/factory", "internal/blocks/core", "internal/gate/core"}, slices.Concat(leaves, blocks, gate)...)
		uses := [][2]string{
			{"cmd/guardprobe", "."}, {".", "internal/factory"}, {".", "internal/plan"}, {"internal/factory", "internal/core"},
			{"internal/blocks/core", "internal/core"}, {"internal/gate/core", "internal/core"},
		}

		for _, leaf := range leaves {
			uses = append(uses, [2]string{"internal/factory", leaf})
		}

		for _, leaf := range []string{"internal/ai", "internal/plan", "internal/tracer", "internal/web"} {
			uses = append(uses, [2]string{leaf, "internal/core"})
		}

		for _, leaf := range blocks {
			uses = append(uses, [2]string{"internal/factory", leaf}, [2]string{leaf, "internal/blocks/core"}, [2]string{leaf, "internal/core"})
		}

		for _, leaf := range gate {
			uses = append(uses, [2]string{"internal/factory", leaf}, [2]string{leaf, "internal/gate/core"}, [2]string{leaf, "internal/core"})
		}

		return starGraph(paths, uses)
	}

	paths := append([]string{".", "cmd/guardprobe", "internal/contracts", "internal/assembly", "internal/executor", "internal/blocks/contracts", "internal/blocks/kit", "internal/gate/contracts", "internal/gate/pipeline"}, slices.Concat(leaves, blocks, gate)...)
	uses := [][2]string{
		{"cmd/guardprobe", "."}, {".", "internal/assembly"}, {".", "internal/plan"}, {"internal/executor", "internal/contracts"},
		{"internal/assembly", "internal/contracts"}, {"internal/assembly", "internal/executor"},
		{"internal/assembly", "internal/blocks/kit"}, {"internal/assembly", "internal/blocks/contracts"}, {"internal/assembly", "internal/gate/pipeline"}, {"internal/assembly", "internal/gate/contracts"},
		{"internal/plan", "internal/executor"},
		{"internal/blocks/contracts", "internal/contracts"}, {"internal/gate/contracts", "internal/contracts"},
		{"internal/blocks/kit", "internal/blocks/contracts"}, {"internal/blocks/kit", "internal/contracts"},
		{"internal/gate/pipeline", "internal/gate/contracts"}, {"internal/gate/pipeline", "internal/contracts"},
	}

	for _, leaf := range leaves {
		uses = append(uses, [2]string{"internal/assembly", leaf})
	}

	for _, leaf := range []string{"internal/ai", "internal/plan", "internal/tracer", "internal/web"} {
		uses = append(uses, [2]string{leaf, "internal/contracts"})
	}

	for _, leaf := range blocks {
		uses = append(uses, [2]string{"internal/blocks/kit", leaf}, [2]string{leaf, "internal/blocks/contracts"})
	}

	for _, leaf := range gate {
		uses = append(uses, [2]string{"internal/gate/pipeline", leaf}, [2]string{leaf, "internal/gate/contracts"})
	}

	return starGraph(paths, uses)
}

func TestProposeStarsReproducesTheGuard(t *testing.T) {
	g := guardGraph(true)
	architecture, text := propose(g, rules.StyleSnowflake)
	want := `version: 1
architecture: snowflake
stars:
  .:
    contracts: ["internal/contracts"]
    assemblers: [".", "cmd/guardprobe", "internal/assembly"]
    engines: ["internal/executor"]
  internal/blocks:
    contracts: ["internal/blocks/contracts"]
    assemblers: ["internal/blocks/kit"]
    engines: []
  internal/gate:
    contracts: ["internal/gate/contracts"]
    assemblers: ["internal/gate/pipeline"]
    engines: []
rules:
  fail_on: [breaking_changes, layer_violation, unclassified, impure_contracts]
`
	if !strings.HasSuffix(text, want) {
		t.Fatalf("the draft must end with\n%s\ngot\n%s", want, text)
	}

	for _, line := range []string{
		"#   .  contracts internal/contracts; assemblers ., cmd/guardprobe, internal/assembly; engines internal/executor; leaves internal/ai, internal/anthropic, internal/plan, internal/tensors, internal/tracer, internal/web; branches internal/blocks, internal/gate\n",
		"#   internal/blocks  branch of .; contracts internal/blocks/contracts; assemblers internal/blocks/kit; engines none; leaves internal/blocks/cleanse, internal/blocks/spotlight, internal/blocks/summarize, internal/blocks/www\n",
		"#   internal/gate  branch of .; contracts internal/gate/contracts; assemblers internal/gate/pipeline; engines none; leaves internal/gate/presence, internal/gate/tagger\n",
		"# treaty check lists no dependency that breaks these rules.\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the header lacks %q:\n%s", line, text)
		}
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	bound, err := architecture.Bind(modulePaths(g))
	if err != nil {
		t.Fatal(err)
	}

	bound.Assign(g)
	if violations := bound.Violations(g); len(violations) > 0 {
		t.Fatalf("the guard's target tree has no violations: %+v", violations)
	}

	if adopted, err := proposal(g, rules.StyleSnowflake); err != nil || adopted != text {
		t.Fatalf("adopt writes the same text: %v", err)
	}
}

func TestProposeStarsOnTheGuardBeforeItsMove(t *testing.T) {
	architecture, text := propose(guardGraph(false), rules.StyleSnowflake)
	want := []rules.Star{
		{Dir: ".", Contracts: []string{"internal/core"}, Assemblers: []string{".", "cmd/guardprobe", "internal/factory"}, Engines: []string{}},
		{Dir: "internal/blocks", Contracts: []string{"internal/blocks/core"}, Assemblers: []string{}, Engines: []string{}},
		{Dir: "internal/gate", Contracts: []string{"internal/gate/core"}, Assemblers: []string{}, Engines: []string{}},
	}

	if !reflect.DeepEqual(architecture.Stars, want) {
		t.Fatalf("stars %+v", architecture.Stars)
	}

	for _, line := range []string{
		"# treaty check lists 6 dependencies that break these rules: 6 into a branch beyond its core.\n",
		"# internal/core, internal/blocks/core and internal/gate/core are contracts by their name. Core is the map's region of a star, not a package: name each for what it holds, such as internal/contracts, and move any assembler or engine in it to a package of its own.\n",
		"# No assemblers in internal/blocks yet: nothing may wire its leaves, since an assembler of star . may use a branch only through its core. Create one, such as internal/blocks/assembly, and build them there.\n",
		"# No assemblers in internal/gate yet: nothing may wire its leaves, since an assembler of star . may use a branch only through its core. Create one, such as internal/gate/assembly, and build them there.\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the draft lacks %q:\n%s", line, text)
		}
	}
}

func TestProposeStarsLooksThroughContainers(t *testing.T) {
	g := starGraph(
		[]string{".", "cmd/a", "cmd/b", "internal/contracts", "internal/x", "pkg/x", "pkg/y"},
		[][2]string{
			{"cmd/a", "."}, {"cmd/b", "."}, {".", "internal/contracts"},
			{"internal/x", "internal/contracts"}, {"pkg/x", "internal/contracts"}, {"pkg/y", "internal/contracts"}, {"pkg/x", "pkg/y"},
		},
	)

	architecture, text := propose(g, rules.StyleSnowflake)
	want := []rules.Star{{Dir: ".", Contracts: []string{"internal/contracts"}, Assemblers: []string{"cmd/a", "cmd/b"}, Engines: []string{"."}}}
	if !reflect.DeepEqual(architecture.Stars, want) {
		t.Fatalf("stars %+v", architecture.Stars)
	}

	for _, line := range []string{
		"#   .  contracts internal/contracts; assemblers cmd/a, cmd/b; engines .; leaves internal/x, pkg/x, pkg/y\n",
		"# treaty check lists 1 dependency that breaks these rules: 1 between sibling spokes.\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the draft lacks %q:\n%s", line, text)
		}
	}

	// A main package that wires two leaves is an assembler.
	g = starGraph(
		[]string{"cmd/app", "internal/contracts", "internal/plan", "internal/web"},
		[][2]string{{"cmd/app", "internal/plan"}, {"cmd/app", "internal/web"}, {"internal/plan", "internal/contracts"}, {"internal/web", "internal/contracts"}},
	)

	if architecture, _ = propose(g, rules.StyleSnowflake); !slices.Equal(architecture.Stars[0].Assemblers, []string{"cmd/app"}) || !slices.Equal(architecture.Stars[0].Contracts, []string{"internal/contracts"}) {
		t.Fatalf("stars %+v", architecture.Stars)
	}
}

func TestProposeStarsWithoutContracts(t *testing.T) {
	g := starGraph(
		[]string{"cmd/x", "lib/a", "lib/b", "lib/c", "lib/d"},
		[][2]string{{"cmd/x", "lib/c"}, {"lib/a", "lib/b"}, {"lib/b", "lib/a"}, {"lib/c", "lib/a"}, {"lib/d", "lib/a"}},
	)

	architecture, text := propose(g, rules.StyleSnowflake)
	if len(architecture.Stars) != 1 || len(architecture.Stars[0].Contracts) != 0 {
		t.Fatalf("stars %+v", architecture.Stars)
	}

	for _, line := range []string{
		"# No contracts in . yet: its spokes may use only its engines and the contracts around it. Create contracts and move what they share there.\n",
		"# Candidates for contracts: lib/a.\n",
		"  .:\n    contracts: []\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the draft lacks %q:\n%s", line, text)
		}
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProposeStarsRefusesAnEmptyTree(t *testing.T) {
	if _, err := proposal(graph.New(), rules.StyleSnowflake); !errors.Is(err, rules.ErrArchitecture) || !strings.Contains(err.Error(), "invalid architecture: found no packages to make a star of, so snowflake does not fit this code; choose another architecture, or write stars in treaty.yaml yourself") {
		t.Fatalf("an empty tree: %v", err)
	}
}

func TestProposeStarsPromotesByTheRules(t *testing.T) {
	// internal/hub uses two spokes, but a spoke uses it: promoting it would
	// hide its sideways dependencies behind an assembler spokes may not use,
	// so it stays a leaf. internal/run is named like an engine and imports
	// only contracts, so it is an engine; internal/wire is named like an
	// assembler and imports a spoke, so it is an assembler.
	g := starGraph(
		[]string{"internal/contracts", "internal/hub", "internal/a", "internal/b", "internal/c", "internal/runner", "internal/wire"},
		[][2]string{
			{"internal/hub", "internal/a"}, {"internal/hub", "internal/b"}, {"internal/c", "internal/hub"},
			{"internal/a", "internal/contracts"}, {"internal/b", "internal/contracts"}, {"internal/c", "internal/contracts"},
			{"internal/runner", "internal/contracts"}, {"internal/a", "internal/runner"},
			{"internal/wire", "internal/a"}, {"internal/wire", "internal/runner"},
		},
	)

	architecture, text := propose(g, rules.StyleSnowflake)
	want := []rules.Star{{Dir: ".", Contracts: []string{"internal/contracts"}, Assemblers: []string{"internal/wire"}, Engines: []string{"internal/runner"}}}
	if !reflect.DeepEqual(architecture.Stars, want) {
		t.Fatalf("stars %+v", architecture.Stars)
	}

	if !strings.Contains(text, "# treaty check lists 3 dependencies that break these rules: 3 between sibling spokes.\n") {
		t.Errorf("the hub's sideways dependencies are counted:\n%s", text)
	}

	// Passing: a hub no spoke uses wires its spokes, so it is an assembler.
	g = starGraph(
		[]string{"internal/contracts", "internal/hub", "internal/a", "internal/b"},
		[][2]string{{"internal/hub", "internal/a"}, {"internal/hub", "internal/b"}, {"internal/a", "internal/contracts"}, {"internal/b", "internal/contracts"}},
	)

	if architecture, _ = propose(g, rules.StyleSnowflake); !slices.Equal(architecture.Stars[0].Assemblers, []string{"internal/hub"}) {
		t.Fatalf("stars %+v", architecture.Stars)
	}
}

func TestProposeStarsListsNestedCorePackages(t *testing.T) {
	// A core role covers exactly the package it names, so a package nested
	// in a core position is listed too.
	g := starGraph(
		[]string{"internal/contracts", "internal/contracts/grades", "internal/assembly", "internal/assembly/parts", "internal/a"},
		[][2]string{{"internal/assembly", "internal/a"}, {"internal/a", "internal/contracts"}, {"internal/contracts", "internal/contracts/grades"}, {"internal/assembly", "internal/assembly/parts"}},
	)

	architecture, _ := propose(g, rules.StyleSnowflake)
	if star := architecture.Stars[0]; !slices.Equal(star.Contracts, []string{"internal/contracts", "internal/contracts/grades"}) || !slices.Equal(star.Assemblers, []string{"internal/assembly", "internal/assembly/parts"}) {
		t.Fatalf("stars %+v", architecture.Stars)
	}
}
