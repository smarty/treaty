package rules

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/graph"
)

// guardPaths are agentportal-guard's packages in its target tree, with a
// few nested ones.
var guardPaths = []string{
	".", "cmd/guardprobe",
	"internal/contracts", "internal/contracts/grades", "internal/assembly", "internal/executor",
	"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web", "internal/web/fetch",
	"internal/blocks/contracts", "internal/blocks/kit", "internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www",
	"internal/gate/contracts", "internal/gate/pipeline", "internal/gate/pipeline/tinylogos", "internal/gate/presence", "internal/gate/tagger",
}

// guardStars are agentportal-guard's stars in its target tree.
func guardStars() Architecture {
	return Architecture{Style: StyleSnowflake, Stars: []Star{
		{Dir: ".", Contracts: []string{"internal/contracts"}, Orchestrators: []string{".", "internal/assembly", "internal/executor"}},
		{Dir: "internal/blocks", Contracts: []string{"internal/blocks/contracts"}, Orchestrators: []string{"internal/blocks/kit"}},
		{Dir: "internal/gate", Contracts: []string{"internal/gate/contracts"}, Orchestrators: []string{"internal/gate/pipeline"}},
	}}
}

// guard is agentportal-guard's target tree, bound.
func guard(t *testing.T) Architecture {
	t.Helper()
	architecture := guardStars()
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	bound, err := architecture.Bind(guardPaths)
	if err != nil {
		t.Fatal(err)
	}

	return bound
}

// deep is the guard with a star below a branch, internal/gate/models.
func deep(t *testing.T) Architecture {
	t.Helper()
	architecture := guardStars()
	architecture.Stars = append(architecture.Stars, Star{Dir: "internal/gate/models", Contracts: []string{"internal/gate/models/contracts"}, Orchestrators: []string{"internal/gate/models/assembly"}})
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	bound, err := architecture.Bind(append(slices.Clone(guardPaths), "internal/gate/models/contracts", "internal/gate/models/assembly", "internal/gate/models/tiny"))
	if err != nil {
		t.Fatal(err)
	}

	return bound
}

// today is agentportal-guard's tree before its move, read with the v2
// rules: what init proposes there.
func today(t *testing.T) (Architecture, []string) {
	t.Helper()
	architecture := Architecture{Style: StyleSnowflake, Stars: []Star{
		{Dir: ".", Contracts: []string{"internal/core"}, Orchestrators: []string{".", "internal/factory"}},
		{Dir: "internal/blocks", Contracts: []string{"internal/blocks/core"}},
		{Dir: "internal/gate", Contracts: []string{"internal/gate/core"}},
	}}

	paths := []string{".", "cmd/guardprobe", "internal/core", "internal/factory", "internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web",
		"internal/blocks/core", "internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www",
		"internal/gate/core", "internal/gate/presence", "internal/gate/tagger"}
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	bound, err := architecture.Bind(paths)
	if err != nil {
		t.Fatal(err)
	}

	return bound, paths
}

func TestSnowflakeResolve(t *testing.T) {
	architecture := guard(t)
	contracts := func(star, entry string) Placement {
		return Placement{Layer: graph.LayerContracts, Star: star, Entry: entry}
	}

	orchestrator := func(star, entry string) Placement {
		return Placement{Layer: graph.LayerOrchestrator, Star: star, Entry: entry}
	}

	leaf := func(star, spoke string) Placement { return Placement{Layer: graph.LayerLeaf, Star: star, Spoke: spoke} }
	for _, c := range []struct {
		path string
		want Placement
	}{
		{".", orchestrator(".", ".")},
		// cmd and internal hold no package, so they are looked through.
		{"cmd/guardprobe", leaf(".", "cmd/guardprobe")},
		{"internal/contracts", contracts(".", "internal/contracts")},
		{"internal/contracts/grades", contracts(".", "internal/contracts")},
		{"internal/assembly", orchestrator(".", "internal/assembly")},
		{"internal/executor", orchestrator(".", "internal/executor")},
		{"internal/plan", leaf(".", "internal/plan")},
		{"internal/web", leaf(".", "internal/web")},
		{"internal/web/fetch", leaf(".", "internal/web")},
		{"internal/blocks/contracts", contracts("internal/blocks", "internal/blocks/contracts")},
		{"internal/blocks/kit", orchestrator("internal/blocks", "internal/blocks/kit")},
		{"internal/blocks/www", leaf("internal/blocks", "internal/blocks/www")},
		{"internal/gate/contracts", contracts("internal/gate", "internal/gate/contracts")},
		{"internal/gate/pipeline", orchestrator("internal/gate", "internal/gate/pipeline")},
		{"internal/gate/pipeline/tinylogos", orchestrator("internal/gate", "internal/gate/pipeline")},
		{"internal/gate/presence", leaf("internal/gate", "internal/gate/presence")},
		{"internal/gate/tagger", leaf("internal/gate", "internal/gate/tagger")},
		// A module not built yet is a leaf of its star.
		{"internal/blocks/new", leaf("internal/blocks", "internal/blocks/new")},
		{"internal/patterns/x", leaf(".", "internal/patterns/x")},
	} {
		if got := architecture.Resolve(c.path); !got.Same(c.want) {
			t.Errorf("Resolve(%s) = %+v, want %+v", c.path, got, c.want)
		}
	}

	// A tree with one deep package: every directory above it is looked
	// through.
	lone, err := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}}.Bind([]string{"internal/x/y/z"})
	if err != nil {
		t.Fatal(err)
	}

	if got := lone.Resolve("internal/x/y/z"); !got.Same(leaf(".", "internal/x/y/z")) {
		t.Errorf("a lone deep package: %+v", got)
	}

	// A star's own package is in its core only when listed.
	owned := guardStars()
	unlisted, err := owned.Bind(append(slices.Clone(guardPaths), "internal/blocks"))
	if err != nil {
		t.Fatal(err)
	}

	if got := unlisted.Resolve("internal/blocks"); !got.Same(Placement{Layer: graph.LayerUnclassified, Star: "internal/blocks"}) {
		t.Errorf("an unlisted own package: %+v", got)
	}

	owned.Stars[1].Contracts = append(owned.Stars[1].Contracts, "internal/blocks")
	listed, err := owned.Bind(append(slices.Clone(guardPaths), "internal/blocks"))
	if err != nil {
		t.Fatal(err)
	}

	if got := listed.Resolve("internal/blocks"); !got.Same(contracts("internal/blocks", "internal/blocks")) {
		t.Errorf("a listed own package: %+v", got)
	}

	// A misfit, looked through in a lenient binding: internal holds a
	// package, yet internal/blocks stays a star.
	misfit, err := guardStars().Bind(append(slices.Clone(guardPaths), "internal"))
	if !errors.Is(err, ErrMisfit) {
		t.Fatalf("misfit: %v", err)
	}

	for path, want := range map[string]Placement{
		"internal":                  {Layer: graph.LayerUnclassified, Star: ".", Spoke: "internal"},
		"internal/plan":             leaf(".", "internal/plan"),
		"internal/blocks/contracts": contracts("internal/blocks", "internal/blocks/contracts"),
		"internal/contracts":        contracts(".", "internal/contracts"),
	} {
		if got := misfit.Resolve(path); !got.Same(want) {
			t.Errorf("lenient Resolve(%s) = %+v, want %+v", path, got, want)
		}
	}

	// Unbound, every directory but the stars, the entries and the path is
	// looked through.
	if got := guardStars().Resolve("internal/web/fetch"); !got.Same(leaf(".", "internal/web/fetch")) {
		t.Errorf("unbound: %+v", got)
	}
}

func TestSnowflakeCheck(t *testing.T) {
	guarded, nested := guard(t), deep(t)
	for _, c := range []struct {
		architecture Architecture
		from, to     string
		rule         string
	}{
		// A leaf uses itself and its own star's core.
		{guarded, "internal/web/fetch", "internal/web", ""},
		{guarded, "internal/blocks/www", "internal/blocks/contracts", ""},
		{guarded, "internal/blocks/www", "internal/blocks/kit", ""},
		{guarded, "internal/plan", "internal/executor", ""},
		{guarded, "cmd/guardprobe", ".", ""},
		// A leaf never uses a sibling leaf, or anything in a branch.
		{guarded, "internal/blocks/cleanse", "internal/blocks/www", "leaf internal/blocks/cleanse may not depend on leaf internal/blocks/www: internal/blocks/cleanse and internal/blocks/www are sibling spokes of star internal/blocks, and spokes never depend sideways; declare a port in the contracts of star internal/blocks (internal/blocks/contracts), implement it in internal/blocks/www, and let an orchestrator of star internal/blocks hand it in"},
		{guarded, "internal/plan", "internal/web", "leaf internal/plan may not depend on leaf internal/web: internal/plan and internal/web are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), implement it in internal/web, and let an orchestrator of star . hand it in"},
		{guarded, "cmd/guardprobe", "internal/plan", "leaf cmd/guardprobe may not depend on leaf internal/plan: cmd/guardprobe and internal/plan are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), implement it in internal/plan, and let an orchestrator of star . hand it in"},
		{guarded, "internal/plan", "internal/blocks/contracts", "leaf internal/plan may not depend on contracts internal/blocks/contracts: internal/plan and internal/blocks are sibling spokes of star ., and spokes never depend sideways, not even on a branch's core; declare what it needs in the contracts of star . (internal/contracts)"},
		{guarded, "internal/plan", "internal/blocks/kit", "leaf internal/plan may not depend on orchestrator internal/blocks/kit: internal/plan and internal/blocks are sibling spokes of star ., and spokes never depend sideways, not even on a branch's core; declare a port in the contracts of star . (internal/contracts), implement it in internal/blocks, and let an orchestrator of star . hand it in"},
		{nested, "internal/gate/tagger", "internal/gate/models/contracts", "leaf internal/gate/tagger may not depend on contracts internal/gate/models/contracts: internal/gate/tagger and internal/gate/models are sibling spokes of star internal/gate, and spokes never depend sideways, not even on a branch's core; declare what it needs in the contracts of star internal/gate (internal/gate/contracts)"},
		{nested, "internal/gate/tagger", "internal/gate/models/tiny", "leaf internal/gate/tagger may not depend on leaf internal/gate/models/tiny: internal/gate/tagger and internal/gate/models are sibling spokes of star internal/gate, and spokes never depend sideways; declare a port in the contracts of star internal/gate (internal/gate/contracts), implement it in internal/gate/models, and let an orchestrator of star internal/gate hand it in"},
		{nested, "internal/plan", "internal/gate/models/contracts", "leaf internal/plan may not depend on contracts internal/gate/models/contracts: internal/plan and internal/gate are sibling spokes of star ., and spokes never depend sideways; declare what it needs in the contracts of star . (internal/contracts)"},
		// A leaf never reaches past its own star's core: one star out, and
		// two.
		{guarded, "internal/blocks/www", "internal/contracts", "leaf internal/blocks/www may not depend on contracts internal/contracts: a spoke may use only the core of its own star, internal/blocks, never the core of star . around it; re-export what it needs in the contracts of star internal/blocks (internal/blocks/contracts)"},
		{guarded, "internal/blocks/summarize", "internal/executor", "leaf internal/blocks/summarize may not depend on orchestrator internal/executor: a spoke may use only the core of its own star, internal/blocks, never the core of star . around it; declare what it needs as a port in the contracts of star . (internal/contracts), re-export it in the contracts of star internal/blocks (internal/blocks/contracts), and let the orchestrators hand internal/executor in"},
		{nested, "internal/gate/models/tiny", "internal/contracts", "leaf internal/gate/models/tiny may not depend on contracts internal/contracts: a spoke may use only the core of its own star, internal/gate/models, never the core of star . around it; re-export what it needs down to the contracts of star internal/gate/models (internal/gate/models/contracts)"},
		{nested, "internal/gate/models/tiny", "internal/assembly", "leaf internal/gate/models/tiny may not depend on orchestrator internal/assembly: a spoke may use only the core of its own star, internal/gate/models, never the core of star . around it; declare what it needs as a port in the contracts of star . (internal/contracts), re-export it down to the contracts of star internal/gate/models (internal/gate/models/contracts), and let the orchestrators hand internal/assembly in"},
		// A leaf of a branch never uses a leaf of the star around it, and
		// a port for it is re-exported down one star, or two.
		{guarded, "internal/blocks/www", "internal/web", "leaf internal/blocks/www may not depend on leaf internal/web: internal/blocks and internal/web are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), re-export it in the contracts of star internal/blocks (internal/blocks/contracts), implement it in internal/web, and let the orchestrators of stars . and internal/blocks hand it in"},
		{nested, "internal/gate/models/tiny", "internal/web", "leaf internal/gate/models/tiny may not depend on leaf internal/web: internal/gate and internal/web are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), re-export it in the contracts of star internal/gate (internal/gate/contracts) and the contracts of star internal/gate/models (internal/gate/models/contracts), implement it in internal/web, and let the orchestrators of stars ., internal/gate and internal/gate/models hand it in"},
		{guarded, "internal/blocks/www", "internal/gate/contracts", "leaf internal/blocks/www may not depend on contracts internal/gate/contracts: internal/blocks and internal/gate are sibling spokes of star ., and spokes never depend sideways, not even on a branch's core; declare what it needs in the contracts of star . (internal/contracts)"},

		// An orchestrator uses its star's whole core; contracts use only
		// its contracts, never an orchestrator.
		{guarded, "internal/executor", "internal/contracts", ""},
		{guarded, "internal/assembly", "internal/executor", ""},
		{guarded, "internal/contracts", "internal/executor", "contracts internal/contracts may not depend on orchestrator internal/executor: contracts never use an orchestrator; declare what it needs in contracts, and let internal/executor implement it"},
		{guarded, "internal/contracts", ".", "contracts internal/contracts may not depend on orchestrator .: contracts never use an orchestrator; declare what it needs in contracts, and let . implement it"},
		{guarded, "internal/gate/contracts", "internal/gate/pipeline", "contracts internal/gate/contracts may not depend on orchestrator internal/gate/pipeline: contracts never use an orchestrator; declare what it needs in contracts, and let internal/gate/pipeline implement it"},
		// Contracts use the contracts one star out and one star in.
		{guarded, "internal/blocks/contracts", "internal/contracts", ""},
		{guarded, "internal/contracts", "internal/blocks/contracts", ""},
		{nested, "internal/gate/contracts", "internal/gate/models/contracts", ""},
		// Contracts never use a spoke.
		{guarded, "internal/contracts", "internal/web", "contracts internal/contracts may not depend on leaf internal/web: contracts never use a spoke; declare what it needs in contracts, and let an orchestrator hand internal/web in"},
		{guarded, "internal/gate/contracts", "internal/gate/tagger", "contracts internal/gate/contracts may not depend on leaf internal/gate/tagger: contracts never use a spoke; declare what it needs in contracts, and let an orchestrator hand internal/gate/tagger in"},
		{guarded, "internal/gate/contracts", "internal/web", "contracts internal/gate/contracts may not depend on leaf internal/web: contracts never use a spoke; declare what it needs in contracts, and let an orchestrator hand internal/web in"},
		{nested, "internal/gate/contracts", "internal/gate/models/tiny", "contracts internal/gate/contracts may not depend on leaf internal/gate/models/tiny: contracts never use a spoke; declare what it needs in contracts, and let an orchestrator hand internal/gate/models/tiny in"},
		// Contracts use a branch only through its contracts, one star in.
		{guarded, "internal/contracts", "internal/gate/pipeline", "contracts internal/contracts may not depend on orchestrator internal/gate/pipeline: contracts may use a branch only through its contracts; declare what it needs in the contracts of star internal/gate (internal/gate/contracts)"},
		{nested, "internal/contracts", "internal/gate/models/contracts", "contracts internal/contracts may not depend on contracts internal/gate/models/contracts: contracts reach only one star in, to the contracts of star internal/gate (internal/gate/contracts); re-export what it needs there"},
		// Contracts use the star around them only through its contracts,
		// one star out.
		{guarded, "internal/gate/contracts", "internal/executor", "contracts internal/gate/contracts may not depend on orchestrator internal/executor: the core of star internal/gate may use the star around it only through its contracts; declare a port for it in the contracts of star . (internal/contracts), and let an orchestrator of star . hand internal/executor in"},
		{nested, "internal/gate/models/contracts", "internal/contracts", "contracts internal/gate/models/contracts may not depend on contracts internal/contracts: the core of star internal/gate/models reaches only one star out, to the contracts of star internal/gate (internal/gate/contracts); re-export what it needs there"},
		{nested, "internal/gate/models/contracts", "internal/assembly", "contracts internal/gate/models/contracts may not depend on orchestrator internal/assembly: the core of star internal/gate/models reaches only one star out, to the contracts of star internal/gate (internal/gate/contracts); declare a port for it in the contracts of star . (internal/contracts), re-export it down to the contracts of star internal/gate (internal/gate/contracts), and let the orchestrators hand internal/assembly in"},
		// Sibling cores never use each other.
		{guarded, "internal/blocks/contracts", "internal/gate/contracts", "contracts internal/blocks/contracts may not depend on contracts internal/gate/contracts: internal/blocks and internal/gate are sibling spokes of star ., and spokes never depend sideways, not even core to core; declare what it needs in the contracts of star . (internal/contracts)"},
		{guarded, "internal/blocks/kit", "internal/gate/pipeline", "orchestrator internal/blocks/kit may not depend on orchestrator internal/gate/pipeline: internal/blocks and internal/gate are sibling spokes of star ., and spokes never depend sideways, not even core to core; declare a port in the contracts of star . (internal/contracts), implement it in internal/gate, and let an orchestrator of star . hand it in"},

		// An orchestrator wires its own leaves and uses its branches' cores
		// and the contracts around it.
		{guarded, "internal/blocks/kit", "internal/blocks/www", ""},
		{guarded, "internal/blocks/kit", "internal/contracts", ""},
		{guarded, ".", "internal/plan", ""},
		{guarded, "internal/assembly", "internal/blocks/kit", ""},
		{guarded, "internal/assembly", "internal/gate/contracts", ""},
		{nested, "internal/gate/pipeline", "internal/gate/models/assembly", ""},
		// Never a branch's spokes, nor anything two stars in.
		{guarded, "internal/assembly", "internal/blocks/www", "orchestrator internal/assembly may not depend on leaf internal/blocks/www: an orchestrator may use a branch only through its core, and internal/blocks/www is a spoke of branch internal/blocks; let an orchestrator of star internal/blocks (internal/blocks/kit) build it, and use that"},
		{nested, "internal/gate/pipeline", "internal/gate/models/tiny", "orchestrator internal/gate/pipeline may not depend on leaf internal/gate/models/tiny: an orchestrator may use a branch only through its core, and internal/gate/models/tiny is a spoke of branch internal/gate/models; let an orchestrator of star internal/gate/models (internal/gate/models/assembly) build it, and use that"},
		{nested, "internal/assembly", "internal/gate/models/contracts", "orchestrator internal/assembly may not depend on contracts internal/gate/models/contracts: an orchestrator reaches only one star in, to the core of branch internal/gate; let the core of star internal/gate stand for it"},
		// Never past the contracts of the star around it, nor a leaf there.
		{guarded, "internal/blocks/kit", "internal/executor", "orchestrator internal/blocks/kit may not depend on orchestrator internal/executor: the core of star internal/blocks may use the star around it only through its contracts; declare a port for it in the contracts of star . (internal/contracts), and let an orchestrator of star . hand internal/executor in"},
		{nested, "internal/gate/models/assembly", "internal/contracts", "orchestrator internal/gate/models/assembly may not depend on contracts internal/contracts: the core of star internal/gate/models reaches only one star out, to the contracts of star internal/gate (internal/gate/contracts); re-export what it needs there"},
		{guarded, "internal/blocks/kit", "internal/web", "orchestrator internal/blocks/kit may not depend on leaf internal/web: internal/blocks and internal/web are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), implement it in internal/web, and let an orchestrator of star . hand it in"},
		{nested, "internal/gate/models/assembly", "internal/assembly", "orchestrator internal/gate/models/assembly may not depend on orchestrator internal/assembly: the core of star internal/gate/models reaches only one star out, to the contracts of star internal/gate (internal/gate/contracts); declare a port for it in the contracts of star . (internal/contracts), re-export it down to the contracts of star internal/gate (internal/gate/contracts), and let the orchestrators hand internal/assembly in"},
		// Nor a leaf of a sibling branch.
		{guarded, "internal/blocks/kit", "internal/gate/tagger", "orchestrator internal/blocks/kit may not depend on leaf internal/gate/tagger: internal/blocks and internal/gate are sibling spokes of star ., and spokes never depend sideways; declare a port in the contracts of star . (internal/contracts), implement it in internal/gate, and let an orchestrator of star . hand it in"},
		{guarded, "internal/blocks/contracts", "internal/gate/tagger", "contracts internal/blocks/contracts may not depend on leaf internal/gate/tagger: contracts never use a spoke; declare what it needs in contracts, and let an orchestrator hand internal/gate/tagger in"},
	} {
		allowed, rule := c.architecture.Check(c.architecture.Resolve(c.from), c.architecture.Resolve(c.to))
		if allowed != (rule == "") || rule != c.rule {
			t.Errorf("Check(%s, %s) = %t, %q\nwant %q", c.from, c.to, allowed, rule, c.rule)
		}
	}

	// A star whose core is empty gets new contracts and a new orchestrator
	// in the advice.
	empty := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Orchestrators: []string{"app"}}, {Dir: "lib"}}}
	for _, c := range []struct{ from, to, rule string }{
		{"lib/a", "lib/b", "leaf lib/a may not depend on leaf lib/b: lib/a and lib/b are sibling spokes of star lib, and spokes never depend sideways; declare a port in new contracts of star lib, such as lib/contracts, implement it in lib/b, and let an orchestrator of star lib hand it in"},
		{"lib/a", "app", "leaf lib/a may not depend on orchestrator app: a spoke may use only the core of its own star, lib, never the core of star . around it; declare what it needs as a port in new contracts of star ., such as contracts, re-export it in new contracts of star lib, such as lib/contracts, and let the orchestrators hand app in"},
		{"app", "lib/a", "orchestrator app may not depend on leaf lib/a: an orchestrator may use a branch only through its core, and lib/a is a spoke of branch lib; let a new orchestrator of star lib, such as lib/assembly, build it, and use that"},
	} {
		if allowed, rule := empty.Check(empty.Resolve(c.from), empty.Resolve(c.to)); allowed || rule != c.rule {
			t.Errorf("Check(%s, %s) = %t, %q\nwant %q", c.from, c.to, allowed, rule, c.rule)
		}
	}
}

func TestSnowflakeBreak(t *testing.T) {
	architecture := deep(t)
	for _, c := range []struct{ from, to, kind string }{
		{"internal/blocks/www", "internal/blocks/contracts", ""},
		{"internal/plan", "internal/web", BreakSideways},
		{"internal/blocks/kit", "internal/gate/pipeline", BreakSideways},
		{"internal/blocks/www", "internal/contracts", BreakOut},
		{"internal/blocks/kit", "internal/executor", BreakOut},
		{"internal/assembly", "internal/blocks/www", BreakIn},
		{"internal/contracts", "internal/gate/pipeline", BreakIn},
		{"internal/contracts", "internal/web", BreakContracts},
		{"internal/contracts", "internal/executor", BreakContracts},
		{"internal/executor", "internal/contracts", ""},
	} {
		if got := architecture.Break(architecture.Resolve(c.from), architecture.Resolve(c.to)); got != c.kind {
			t.Errorf("Break(%s, %s) = %q, want %q", c.from, c.to, got, c.kind)
		}
	}

	unclassified := Placement{Layer: graph.LayerUnclassified, Star: "internal/blocks"}
	if got := architecture.Break(unclassified, architecture.Resolve("internal/web")); got != "" {
		t.Errorf("an unclassified end is not checked: %q", got)
	}

	if got := (Architecture{Style: StyleHexagonal}).Break(Placement{Layer: graph.LayerDomain}, Placement{Layer: graph.LayerAdapter}); got != "" {
		t.Errorf("only a snowflake has break kinds: %q", got)
	}
}

func TestSnowflakeViolations(t *testing.T) {
	architecture := guardStars()
	g := graph.New()
	for _, path := range guardPaths {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	uses("internal/plan", "internal/web")
	uses("internal/plan", "internal/executor")
	uses("internal/blocks/www", "internal/blocks/contracts")
	uses("internal/blocks/www", "internal/contracts")
	uses("internal/blocks/kit", "internal/blocks/www")
	uses("internal/blocks/contracts", "internal/contracts")
	uses("internal/assembly", "internal/blocks/kit")
	uses("internal/assembly", "internal/gate/tagger")
	uses("internal/contracts", "internal/web")
	uses("internal/gate/pipeline", "internal/gate/tagger")
	uses("cmd/guardprobe", ".")
	g.Normalize()
	architecture.Assign(g)

	var got [][2]string
	for _, violation := range architecture.Violations(g) {
		if violation.Kind != FindingLayerViolation {
			t.Errorf("only layer violations: %+v", violation)
		}

		got = append(got, [2]string{violation.From, violation.To})
	}

	want := [][2]string{
		{"go:internal/assembly", "go:internal/gate/tagger"},
		{"go:internal/blocks/www", "go:internal/contracts"},
		{"go:internal/contracts", "go:internal/web"},
		{"go:internal/plan", "go:internal/web"},
	}

	if !slices.Equal(got, want) {
		t.Errorf("violations %v, want %v", got, want)
	}

	bound, _ := architecture.Bind(guardPaths)
	for _, module := range g.Modules {
		if want := bound.Resolve(module.Path); !placementOf(module).Same(want) {
			t.Errorf("%s: Assign gave %+v, Resolve %+v", module.Path, placementOf(module), want)
		}
	}
}

func TestSnowflakeViolationsOnTheGuardBeforeItsMove(t *testing.T) {
	architecture, paths := today(t)
	g := graph.New()
	for _, path := range paths {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	branchLeaves := []string{"internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www", "internal/gate/presence", "internal/gate/tagger"}
	for _, leaf := range branchLeaves {
		uses(leaf, "internal/core")
		uses(leaf, leaf[:strings.LastIndex(leaf, "/")]+"/core")
		uses("internal/factory", leaf)
	}

	for _, leaf := range []string{"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web"} {
		uses(leaf, "internal/core")
		uses("internal/factory", leaf)
	}

	uses("internal/factory", "internal/core")
	uses("internal/blocks/core", "internal/core")
	uses("internal/gate/core", "internal/core")
	uses(".", "internal/factory")
	uses("cmd/guardprobe", ".")
	g.Normalize()
	architecture.Assign(g)

	rules := map[[2]string]string{}
	for _, violation := range architecture.Violations(g) {
		rules[[2]string{violation.From, violation.To}] = violation.Rule
	}

	if len(rules) != 12 {
		t.Errorf("%d violations, want 12: %v", len(rules), rules)
	}

	for _, leaf := range branchLeaves {
		if rules[[2]string{"go:" + leaf, "go:internal/core"}] == "" || rules[[2]string{"go:internal/factory", "go:" + leaf}] == "" {
			t.Errorf("%s: missing its two violations", leaf)
		}
	}

	for edge, want := range map[[2]string]string{
		{"go:internal/blocks/www", "go:internal/core"}:       "leaf internal/blocks/www may not depend on contracts internal/core: a spoke may use only the core of its own star, internal/blocks, never the core of star . around it; re-export what it needs in the contracts of star internal/blocks (internal/blocks/core)",
		{"go:internal/gate/tagger", "go:internal/core"}:      "leaf internal/gate/tagger may not depend on contracts internal/core: a spoke may use only the core of its own star, internal/gate, never the core of star . around it; re-export what it needs in the contracts of star internal/gate (internal/gate/core)",
		{"go:internal/factory", "go:internal/blocks/www"}:    "orchestrator internal/factory may not depend on leaf internal/blocks/www: an orchestrator may use a branch only through its core, and internal/blocks/www is a spoke of branch internal/blocks; let a new orchestrator of star internal/blocks, such as internal/blocks/assembly, build it, and use that",
		{"go:internal/factory", "go:internal/gate/presence"}: "orchestrator internal/factory may not depend on leaf internal/gate/presence: an orchestrator may use a branch only through its core, and internal/gate/presence is a spoke of branch internal/gate; let a new orchestrator of star internal/gate, such as internal/gate/assembly, build it, and use that",
	} {
		if rules[edge] != want {
			t.Errorf("%v: got %q\nwant %q", edge, rules[edge], want)
		}
	}
}

func TestSnowflakeValidate(t *testing.T) {
	root := func(contracts, orchestrators []string, more ...Star) Architecture {
		return Architecture{Style: StyleSnowflake, Stars: append([]Star{{Dir: ".", Contracts: contracts, Orchestrators: orchestrators}}, more...)}
	}

	for _, c := range []struct {
		name         string
		architecture Architecture
		says         string
	}{
		{"composition", Architecture{Style: StyleSnowflake, Composition: []string{"cmd/**"}, Stars: []Star{{Dir: "."}}}, "composition does not apply to the snowflake architecture: orchestrators do its work, so list main packages, factories and the public API among the orchestrators of their star"},
		{"layers", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}, Layers: []Layer{{Name: "web"}}}, "layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share"},
		{"shared", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}, Shared: []string{"lib/**"}}, "layers, shared, boundaries and public do not apply"},
		{"no stars", Architecture{Style: StyleSnowflake}, `the snowflake architecture needs its stars: declare the repository, ".", with its contracts and orchestrators, such as .: {contracts: ["internal/contracts"], orchestrators: ["."]}`},
		{"a glob for a star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}, {Dir: "internal/*"}}}, `star "internal/*" must be a directory path relative to the repository root, not a glob`},
		{"a star above the root", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "../other"}}}, `star "../other" must be a directory path relative to the repository root, not a glob`},
		{"an absolute star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "/internal"}}}, `star "/internal" must be a directory path relative`},
		{"a dotted star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "./internal"}}}, `star "./internal" must be a directory path relative`},
		{"a star listed twice", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}, {Dir: "internal"}, {Dir: "internal"}}}, "star internal is listed twice"},
		{"no root", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal/gate"}, {Dir: "internal/blocks"}, {Dir: "internal/blocks/x"}}}, `the snowflake's root star is the repository itself: declare "." under stars, around internal/blocks and internal/gate`},
		{"an unclean entry", root([]string{"./contracts"}, nil), `contracts entry "./contracts" of star . must be a directory path relative to the repository root, with no empty, . or .. segment`},
		{"an entry with a trailing slash", root([]string{"contracts/"}, nil), `contracts entry "contracts/" of star . must be a directory path relative`},
		{"an entry that climbs out", root(nil, nil, Star{Dir: "internal", Contracts: []string{"internal/../contracts"}}), `contracts entry "internal/../contracts" of star internal must be a directory path relative`},
		{"a glob entry", root(nil, []string{"cmd/*"}), `orchestrators entry "cmd/*" of star . is a glob; name each package by its directory, so a new directory stays a spoke until treaty.yaml names it`},
		{"a recursive glob entry", root([]string{"internal/core/**"}, nil), `contracts entry "internal/core/**" of star . is a glob`},
		{"an entry outside its star", root(nil, nil, Star{Dir: "internal", Contracts: []string{"pkg/contracts"}}), `contracts entry "pkg/contracts" of star internal lies outside internal; a star's core is inside its directory`},
		{"an entry inside a branch", root([]string{"internal/blocks/contracts"}, nil, Star{Dir: "internal/blocks"}), `contracts entry "internal/blocks/contracts" of star . lies inside branch internal/blocks, whose core is its own; list it under internal/blocks`},
		{"an entry that is a branch", root(nil, []string{"internal/blocks"}, Star{Dir: "internal/blocks"}), `orchestrators entry "internal/blocks" of star . lies inside branch internal/blocks`},
		{"an entry holding a star", root(nil, []string{"internal"}, Star{Dir: "internal/blocks"}), `orchestrators entry "internal" of star . holds star internal/blocks; a core package is one package and those nested in it, never a star`},
		{"an entry holding an entry", root([]string{"internal/contracts", "internal/contracts/grades"}, nil), `contracts entry "internal/contracts" of star . holds entry internal/contracts/grades, which it covers already; drop the inner one`},
		{"an entry holding an orchestrator", root([]string{"internal/contracts"}, []string{"internal/contracts/run"}), `contracts entry "internal/contracts" of star . holds entry internal/contracts/run`},
		{"an entry listed twice", root(nil, []string{"internal/executor", "internal/executor"}), `orchestrators entry "internal/executor" of star . is listed twice`},
		{"an entry in both lists", root([]string{"internal/executor"}, []string{"internal/executor"}), `"internal/executor" is listed under both the contracts and the orchestrators of star .; a core package is one or the other`},
		{"stars under hexagonal", Architecture{Style: StyleHexagonal, Stars: []Star{{Dir: "."}}}, "stars apply only to the snowflake architecture"},
		{"stars under none", Architecture{Style: StyleNone, Stars: []Star{{Dir: "."}}}, "stars apply only to the snowflake architecture"},
	} {
		err := c.architecture.Validate()
		if !errors.Is(err, ErrArchitecture) || !strings.HasPrefix(err.Error(), "invalid architecture: "+c.says) {
			t.Errorf("%s: got %v\nwant %s", c.name, err, c.says)
		}
	}

	for _, valid := range []Architecture{
		guardStars(),
		{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Contracts: []string{"."}}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Orchestrators: []string{"."}}, {Dir: "internal/billing"}, {Dir: "internal/orders", Contracts: []string{"internal/orders/ports"}, Orchestrators: []string{"internal/orders"}}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Contracts: []string{"internal/web/port"}}}},
		// A file-routed directory's brackets are part of its name.
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Orchestrators: []string{"app/[slug]"}}, {Dir: "app/[lang]"}}},
	} {
		if err := valid.Validate(); err != nil {
			t.Errorf("%+v: %v", valid.Stars, err)
		}
	}
}

func TestSnowflakeBind(t *testing.T) {
	port, container := guardStars(), guardStars()
	port.Stars[0].Contracts = append(port.Stars[0].Contracts, "internal/web/port")
	container.Stars[0].Orchestrators = append(container.Stars[0].Orchestrators, "cmd")
	for _, c := range []struct {
		name  string
		stars Architecture
		paths []string
		says  string
		short string
	}{
		{"a star below a package", port, append(slices.Clone(guardPaths), "internal", "internal/web/port"),
			"star internal/blocks lies inside spoke internal of star .: internal holds a package, so it is a leaf holding everything below it; declare internal a star, or move its package into a child directory",
			"star internal/blocks lies inside spoke internal of star ."},
		{"an entry below a package", port, append(slices.Clone(guardPaths), "internal/web/port"),
			"contracts entry internal/web/port of star . lies inside spoke internal/web: a core package is a whole child of its star, never part of a spoke; move the package out of internal/web, or declare internal/web a star with internal/web/port in its contracts",
			"contracts entry internal/web/port of star . lies inside spoke internal/web"},
		{"an entry naming a container", container, append(slices.Clone(guardPaths), "cmd/a", "cmd/b", "cmd/b/x"),
			"orchestrators entry cmd of star . holds no package of its own, only cmd/a, cmd/b and cmd/guardprobe below it; name each package, so a new one stays a spoke until treaty.yaml names it",
			"orchestrators entry cmd of star . holds no package of its own"},
	} {
		bound, err := c.stars.Bind(c.paths)
		if !errors.Is(err, ErrArchitecture) || !errors.Is(err, ErrMisfit) || err.Error() != "invalid architecture: the stars do not fit the code: "+c.says {
			t.Errorf("%s: got %v\nwant %s", c.name, err, c.says)
		}

		if bound.Misfit() != c.short {
			t.Errorf("%s: misfit %q, want %q", c.name, bound.Misfit(), c.short)
		}

		// Bound all the same, leniently: every module is placed.
		for _, modulePath := range c.paths {
			if placement := bound.Resolve(modulePath); placement.Star == "" {
				t.Errorf("%s: %s is placed nowhere", c.name, modulePath)
			}
		}
	}

	// A container entry still claims what lies under it.
	bound, _ := container.Bind(append(slices.Clone(guardPaths), "cmd/a"))
	if got := bound.Resolve("cmd/a"); !got.Same(Placement{Layer: graph.LayerOrchestrator, Star: ".", Entry: "cmd"}) {
		t.Errorf("a container entry: %+v", got)
	}

	// Stars and entries declared before their code exists bind.
	ahead := guardStars()
	ahead.Stars = append(ahead.Stars, Star{Dir: "internal/billing", Contracts: []string{"internal/billing/contracts"}})
	if _, err := ahead.Bind(guardPaths); err != nil {
		t.Errorf("a star not built yet: %v", err)
	}

	if bound, err := (Architecture{Style: StyleLayered}).Bind([]string{"x"}); err != nil || bound.Misfit() != "" {
		t.Errorf("other styles bind as they are: %v", err)
	}
}

func TestSnowflakeGuidanceMayUseSummary(t *testing.T) {
	architecture := guard(t)
	for _, c := range []struct {
		path     string
		guidance string
		mayUse   []string
	}{
		{".", "Orchestrator . of star .: wires its star. It may use the core and the leaves of star . and the cores of its branches internal/blocks and internal/gate, never a branch's spokes.",
			[]string{"contracts internal/contracts", "orchestrator .", "orchestrator internal/assembly", "orchestrator internal/executor", "the leaves of star .", "contracts internal/blocks/contracts", "orchestrator internal/blocks/kit", "contracts internal/gate/contracts", "orchestrator internal/gate/pipeline"}},
		{"internal/blocks/kit", "Orchestrator internal/blocks/kit of star internal/blocks, a branch of .: wires its star. It may use the core and the leaves of star internal/blocks and the contracts of star . around it, nothing else.",
			[]string{"contracts internal/blocks/contracts", "orchestrator internal/blocks/kit", "the leaves of star internal/blocks", "contracts internal/contracts"}},
		{"internal/contracts", "Contracts internal/contracts of star .: interfaces, value types and their pure methods, errors and constants. They may use the contracts of star . and the contracts of its branches internal/blocks and internal/gate, never a spoke or an orchestrator.",
			[]string{"contracts internal/contracts", "contracts internal/blocks/contracts", "contracts internal/gate/contracts"}},
		{"internal/blocks/contracts", "Contracts internal/blocks/contracts of star internal/blocks, a branch of .: interfaces, value types and their pure methods, errors and constants, and what its spokes need from star ., re-exported. They may use the contracts of star internal/blocks and the contracts of star . around it, never a spoke or an orchestrator.",
			[]string{"contracts internal/blocks/contracts", "contracts internal/contracts"}},
		{"internal/plan", "Leaf internal/plan of star .: may use itself and the core of star ., never a sibling spoke. To use a sibling, depend on a port in the contracts of star . and let an orchestrator of star . hand the sibling in.",
			[]string{"leaf internal/plan", "contracts internal/contracts", "orchestrator .", "orchestrator internal/assembly", "orchestrator internal/executor"}},
		{"internal/blocks/www", "Leaf internal/blocks/www of star internal/blocks, a branch of .: may use itself and the core of star internal/blocks, never a sibling spoke or the core of star . around it. To use a sibling, depend on a port in the contracts of star internal/blocks and let an orchestrator of star internal/blocks hand the sibling in; what it needs from star . comes re-exported through those contracts.",
			[]string{"leaf internal/blocks/www", "contracts internal/blocks/contracts", "orchestrator internal/blocks/kit"}},
	} {
		placement := architecture.Resolve(c.path)
		if got := architecture.Guidance(placement); got != c.guidance {
			t.Errorf("Guidance(%s) = %q\nwant %q", c.path, got, c.guidance)
		}

		if got := architecture.MayUse(placement); !slices.Equal(got, c.mayUse) {
			t.Errorf("MayUse(%s) = %q\nwant %q", c.path, got, c.mayUse)
		}
	}

	empty := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}, {Dir: "lib"}}}
	for placement, want := range map[*Placement]string{
		{Layer: graph.LayerLeaf, Star: "lib", Spoke: "lib/a"}:                "Leaf lib/a of star lib, a branch of .: may use only itself, since star lib has no core yet; list its contracts and orchestrators under stars in treaty.yaml.",
		{Layer: graph.LayerUnclassified, Star: "internal/blocks"}:            "Unclassified: the own package of star internal/blocks holds code, so treaty.yaml must list it in that star's contracts or orchestrators; until then its dependencies are not checked.",
		{Layer: graph.LayerUnclassified, Star: ".", Spoke: "internal"}:       "Unclassified: internal holds a package where treaty.yaml's stars look through it, so its dependencies are not checked.",
		{Layer: graph.LayerLeaf, Star: ".", Spoke: "app"}:                    "Leaf app of star .: may use only itself, since star . has no core yet; list its contracts and orchestrators under stars in treaty.yaml.",
		{Layer: graph.LayerOrchestrator, Star: "lib", Entry: "lib/assembly"}: "Orchestrator lib/assembly of star lib, a branch of .: wires its star. It may use the core and the leaves of star lib and the contracts of star . around it, nothing else.",
	} {
		if got := empty.Guidance(*placement); got != want {
			t.Errorf("Guidance(%+v) = %q\nwant %q", *placement, got, want)
		}
	}

	if got := architecture.MayUse(Placement{Layer: graph.LayerUnclassified, Star: "internal/blocks"}); !slices.Equal(got, []string{"every module, unchecked"}) {
		t.Errorf("an unclassified module may use %q", got)
	}

	// Stars are listed root first, whatever order treaty.yaml gives.
	slices.Reverse(architecture.Stars)
	want := "Rules (snowflake): the repository is a star of stars. Each star has a core, holding its contracts and its orchestrators, and spokes, its other children that hold a package, found by looking through directories that hold none: a spoke is a leaf, or a branch, a star of its own. " +
		"A spoke may use only its own star's core. Contracts may use the contracts of their own star, of the star around it and of its branches, never a spoke and never an orchestrator; an orchestrator may use its own star's core, its star's leaves, its branches' cores and the contracts of the star around it, never a branch's spokes. " +
		"Nothing else: no dependency runs sideways, not even between the cores of sibling branches. " +
		"Stars: . (contracts internal/contracts; orchestrators ., internal/assembly and internal/executor), internal/blocks (branch of .; contracts internal/blocks/contracts; orchestrators internal/blocks/kit), internal/gate (branch of .; contracts internal/gate/contracts; orchestrators internal/gate/pipeline)."
	if got := architecture.Summary(); got != want {
		t.Errorf("Summary() = %q\nwant %q", got, want)
	}

	if got := empty.Summary(); !strings.HasSuffix(got, "Stars: . (no contracts; no orchestrators), lib (branch of .; no contracts; no orchestrators).") {
		t.Errorf("Summary() of empty cores = %q", got)
	}

	if names := architecture.LayerNames(); names != nil {
		t.Errorf("a snowflake has no layer names: %v", names)
	}
}

func TestSnowflakeReasons(t *testing.T) {
	architecture := guard(t)
	for _, c := range []struct {
		path string
		want []Reason
	}{
		{"internal/contracts/grades", []Reason{{[]string{"stars", ".", "contracts"}, "internal/contracts"}}},
		{".", []Reason{{[]string{"stars", ".", "orchestrators"}, "."}}},
		{"internal/blocks/kit", []Reason{{[]string{"stars", "internal/blocks", "orchestrators"}, "internal/blocks/kit"}}},
		{"internal/web/fetch", []Reason{{[]string{"stars", "."}, ""}}},
		{"internal/blocks/www", []Reason{{[]string{"stars", "internal/blocks"}, ""}}},
		{"internal/blocks", nil},
	} {
		got := architecture.Reasons(c.path)
		if !slices.EqualFunc(got, c.want, func(a, b Reason) bool { return a.Glob == b.Glob && slices.Equal(a.Path, b.Path) }) {
			t.Errorf("%s: got %+v, want %+v", c.path, got, c.want)
		}
	}
}

func TestSnowflakeTree(t *testing.T) {
	architecture := guard(t)
	slices.Reverse(architecture.Stars)
	want := []StarNode{
		{Dir: ".", Depth: 0, Contracts: []string{"internal/contracts"}, Orchestrators: []string{".", "internal/assembly", "internal/executor"},
			Leaves: []string{"cmd/guardprobe", "internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web"}, Branches: []string{"internal/blocks", "internal/gate"}},
		{Dir: "internal/blocks", Parent: ".", Depth: 1, Contracts: []string{"internal/blocks/contracts"}, Orchestrators: []string{"internal/blocks/kit"},
			Leaves: []string{"internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www"}, Branches: []string{}},
		{Dir: "internal/gate", Parent: ".", Depth: 1, Contracts: []string{"internal/gate/contracts"}, Orchestrators: []string{"internal/gate/pipeline"},
			Leaves: []string{"internal/gate/presence", "internal/gate/tagger"}, Branches: []string{}},
	}

	if got := architecture.Tree(); !reflect.DeepEqual(got, want) {
		t.Errorf("Tree = %+v\nwant %+v", got, want)
	}

	// Each star comes before the stars inside it, even when a sibling's
	// name sorts between them.
	nested := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "a-b"}, {Dir: "a/b"}, {Dir: "a"}, {Dir: "."}}}
	var dirs []string
	for _, node := range nested.Tree() {
		dirs = append(dirs, node.Dir)
	}

	if !slices.Equal(dirs, []string{".", "a", "a/b", "a-b"}) {
		t.Errorf("order %v", dirs)
	}

	if (Architecture{Style: StyleLayered}).Tree() != nil {
		t.Error("only a snowflake has a star tree")
	}
}

func TestSnowflakeFindings(t *testing.T) {
	architecture := guardStars()
	base, head := graph.New(), graph.New()
	for _, path := range []string{"internal/blocks", "internal/blocks/contracts", "internal/blocks/kit", "internal/blocks/www"} {
		head.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path, Files: []string{path + "/x.go"}})
	}

	head.Normalize()
	base.Normalize()
	architecture.Assign(head)
	details := map[string]string{}
	for _, finding := range Rank(head, base, nil, nil) {
		details[finding.Kind+" "+finding.Target] = finding.Title + ": " + finding.Detail
	}

	for key, want := range map[string]string{
		"new_module go:internal/blocks/contracts": "New module go:internal/blocks/contracts: Placed in contracts internal/blocks/contracts of star internal/blocks.",
		"new_module go:internal/blocks/kit":       "New module go:internal/blocks/kit: Placed in orchestrator internal/blocks/kit of star internal/blocks.",
		"new_module go:internal/blocks/www":       "New module go:internal/blocks/www: Placed in leaf internal/blocks/www of star internal/blocks.",
		"unclassified go:internal/blocks":         "go:internal/blocks is unclassified: treaty.yaml places this module nowhere, so its dependencies are not checked.",
	} {
		if details[key] != want {
			t.Errorf("%s: got %q\nwant %q", key, details[key], want)
		}
	}
}

func TestSnowflakeNotes(t *testing.T) {
	architecture := guardStars()
	architecture.Stars = append(architecture.Stars, Star{Dir: "internal/blcoks", Contracts: []string{"internal/blcoks/contracts"}}, Star{Dir: "internal/billing"})
	architecture.Stars[2].Contracts = append(architecture.Stars[2].Contracts, "internal/gate/ports")
	architecture.Stars[2].Orchestrators = append(architecture.Stars[2].Orchestrators, "internal/gate/run")
	bound, err := architecture.Bind(append(slices.Clone(guardPaths), "internal/blocks", "internal/billing/a", "internal/billing/b"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"star internal/billing has no contracts and no orchestrators, so its spokes may use nothing outside themselves",
		"treaty.yaml declares star internal/blcoks under stars, but no module lies in it; check the directory's spelling",
		"star internal/blocks holds a package of its own, internal/blocks, which treaty.yaml lists in neither its contracts nor its orchestrators, so it is unclassified; list it in one of them",
		"contracts entry internal/gate/ports of star internal/gate matches no module; check its spelling",
		"orchestrators entry internal/gate/run of star internal/gate matches no module; check its spelling",
	}

	if got := bound.Notes(); !slices.Equal(got, want) {
		t.Errorf("Notes = %q\nwant %q", got, want)
	}

	if got := (Architecture{Style: StyleLayered, Layers: []Layer{{Name: "web", Globs: []string{"web/**"}}}}).Notes(); got != nil {
		t.Errorf("only a snowflake has stars: %q", got)
	}
}

func TestSnowflakeCrossingPositionInnermost(t *testing.T) {
	architecture := deep(t)
	for _, c := range []struct{ from, to, star, child string }{
		{"internal/plan", "internal/web", ".", "internal/web"},
		{"internal/plan", "internal/blocks/contracts", ".", "internal/blocks"},
		{"internal/gate/tagger", "internal/blocks/www", ".", "internal/blocks"},
		{"internal/blocks/cleanse", "internal/blocks/www", "internal/blocks", "internal/blocks/www"},
		{"internal/blocks/kit", "internal/executor", ".", "internal/executor"},
		{"internal/assembly", "internal/gate/models/contracts", ".", "internal/gate"},
		{"internal/gate/tagger", "internal/gate/models/tiny", "internal/gate", "internal/gate/models"},
	} {
		star, child := architecture.Crossing(architecture.Resolve(c.from), architecture.Resolve(c.to))
		if star != c.star || child != c.child {
			t.Errorf("Crossing(%s, %s) = %s, %s; want %s, %s", c.from, c.to, star, child, c.star, c.child)
		}
	}

	for path, want := range map[string]string{
		".": ".", "internal/contracts/grades": "internal/contracts", "internal/web/fetch": "internal/web", "internal/blocks": "internal/blocks",
		"internal/gate/pipeline/tinylogos": "internal/gate/pipeline", "cmd/guardprobe": "cmd/guardprobe", "internal/new": "internal/new",
	} {
		if got := architecture.Position(path); got != want {
			t.Errorf("Position(%s) = %q, want %q", path, got, want)
		}
	}

	for path, want := range map[string]string{".": ".", "internal/assembly": ".", "internal/blocks": "internal/blocks", "internal/gate/tagger/x": "internal/gate", "internal/gate/models/tiny": "internal/gate/models"} {
		if got := architecture.Innermost(path); got != want {
			t.Errorf("Innermost(%s) = %q, want %q", path, got, want)
		}
	}

	for star, want := range map[string]string{".": "", "internal/blocks": ".", "internal/gate/models": "internal/gate"} {
		if got := architecture.Parent(star); got != want {
			t.Errorf("Parent(%s) = %q, want %q", star, got, want)
		}
	}

	if got := (Architecture{Style: StyleHexagonal}).Innermost("internal"); got != "" {
		t.Errorf("only a snowflake has stars: %q", got)
	}
}

func TestSnowflakeHandover(t *testing.T) {
	architecture := deep(t)
	for _, c := range []struct {
		from, to, declare string
		reexport          []string
		hands             string
	}{
		{"internal/plan", "internal/web", "the contracts of star . (internal/contracts)", nil, "an orchestrator of star ."},
		{"internal/blocks/www", "internal/web", "the contracts of star . (internal/contracts)", []string{"the contracts of star internal/blocks (internal/blocks/contracts)"}, "the orchestrators of stars . and internal/blocks"},
		{"internal/blocks/summarize", "internal/executor", "the contracts of star . (internal/contracts)", []string{"the contracts of star internal/blocks (internal/blocks/contracts)"}, "the orchestrators of stars . and internal/blocks"},
		{"internal/blocks/kit", "internal/gate/pipeline", "the contracts of star . (internal/contracts)", nil, "an orchestrator of star ."},
		{"internal/gate/models/tiny", "internal/web", "the contracts of star . (internal/contracts)", []string{"the contracts of star internal/gate (internal/gate/contracts)", "the contracts of star internal/gate/models (internal/gate/models/contracts)"}, "the orchestrators of stars ., internal/gate and internal/gate/models"},
	} {
		declare, reexport, hands := architecture.Handover(architecture.Resolve(c.from), architecture.Resolve(c.to))
		if declare != c.declare || !slices.Equal(reexport, c.reexport) || hands != c.hands {
			t.Errorf("Handover(%s, %s) = %q, %q, %q", c.from, c.to, declare, reexport, hands)
		}
	}
}
