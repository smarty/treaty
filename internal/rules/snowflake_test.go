package rules

import (
	"errors"
	"fmt"
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

// guardStars are agentportal-guard's stars in its target tree, under the
// third snowflake's roles.
func guardStars() Architecture {
	return Architecture{Style: StyleSnowflake, Stars: []Star{
		{Dir: ".", Contracts: []string{"internal/contracts", "internal/contracts/grades"}, Assemblers: []string{".", "cmd/guardprobe", "internal/assembly"}, Engines: []string{"internal/executor"}},
		{Dir: "internal/blocks", Contracts: []string{"internal/blocks/contracts"}, Assemblers: []string{"internal/blocks/kit"}},
		{Dir: "internal/gate", Contracts: []string{"internal/gate/contracts"}, Assemblers: []string{"internal/gate/pipeline"}},
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

// deep is the guard with a star below a branch, internal/gate/models, and
// an engine in internal/gate.
func deep(t *testing.T) Architecture {
	t.Helper()
	architecture := guardStars()
	architecture.Stars[2].Engines = []string{"internal/gate/score"}
	architecture.Stars = append(architecture.Stars, Star{Dir: "internal/gate/models", Contracts: []string{"internal/gate/models/contracts"}, Assemblers: []string{"internal/gate/models/assembly"}, Engines: []string{"internal/gate/models/run"}})
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	bound, err := architecture.Bind(append(slices.Clone(guardPaths), "internal/gate/score", "internal/gate/models/contracts", "internal/gate/models/assembly", "internal/gate/models/run", "internal/gate/models/tiny"))
	if err != nil {
		t.Fatal(err)
	}

	return bound
}

func TestSnowflakeResolve(t *testing.T) {
	architecture := guard(t)
	core := func(layer, star, entry string) Placement { return Placement{Layer: layer, Star: star, Entry: entry} }
	contracts := func(star, entry string) Placement { return core(graph.LayerContracts, star, entry) }
	assembler := func(star, entry string) Placement { return core(graph.LayerAssembler, star, entry) }
	leaf := func(star, spoke string) Placement { return Placement{Layer: graph.LayerLeaf, Star: star, Spoke: spoke} }
	for _, c := range []struct {
		path string
		want Placement
	}{
		{".", assembler(".", ".")},
		{"cmd/guardprobe", assembler(".", "cmd/guardprobe")},
		{"internal/contracts", contracts(".", "internal/contracts")},
		// A nested package listed itself has its own entry.
		{"internal/contracts/grades", contracts(".", "internal/contracts/grades")},
		{"internal/assembly", assembler(".", "internal/assembly")},
		{"internal/executor", core(graph.LayerEngine, ".", "internal/executor")},
		// cmd and internal hold no package, so they are looked through.
		{"internal/plan", leaf(".", "internal/plan")},
		{"internal/web", leaf(".", "internal/web")},
		{"internal/web/fetch", leaf(".", "internal/web")},
		{"internal/blocks/contracts", contracts("internal/blocks", "internal/blocks/contracts")},
		{"internal/blocks/kit", assembler("internal/blocks", "internal/blocks/kit")},
		{"internal/blocks/www", leaf("internal/blocks", "internal/blocks/www")},
		{"internal/gate/pipeline", assembler("internal/gate", "internal/gate/pipeline")},
		// A package beneath a core package that no entry names is
		// unclassified, with the core package it lies beneath.
		{"internal/gate/pipeline/tinylogos", Placement{Layer: graph.LayerUnclassified, Star: "internal/gate", Entry: "internal/gate/pipeline"}},
		{"internal/gate/presence", leaf("internal/gate", "internal/gate/presence")},
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

	// A star's own package is in its core only when listed, and the
	// packages under a star's own directory listed as core are its spokes.
	owned := guardStars()
	unlisted, err := owned.Bind(append(slices.Clone(guardPaths), "internal/blocks"))
	if err != nil {
		t.Fatal(err)
	}

	if got := unlisted.Resolve("internal/blocks"); !got.Same(Placement{Layer: graph.LayerUnclassified, Star: "internal/blocks"}) {
		t.Errorf("an unlisted own package: %+v", got)
	}

	owned.Stars[1].Assemblers = append(owned.Stars[1].Assemblers, "internal/blocks")
	listed, err := owned.Bind(append(slices.Clone(guardPaths), "internal/blocks"))
	if err != nil {
		t.Fatal(err)
	}

	if got := listed.Resolve("internal/blocks"); !got.Same(assembler("internal/blocks", "internal/blocks")) {
		t.Errorf("a listed own package: %+v", got)
	}

	if got := listed.Resolve("internal/blocks/www"); !got.Same(leaf("internal/blocks", "internal/blocks/www")) {
		t.Errorf("a spoke under a star's own core package: %+v", got)
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

func TestSnowflakeNestedUnderCore(t *testing.T) {
	stars := guardStars()
	paths := append(slices.Clone(guardPaths), "internal/blocks/kit/sub")
	bound, err := stars.Bind(paths)
	if err != nil {
		t.Fatal(err)
	}

	want := Placement{Layer: graph.LayerUnclassified, Star: "internal/blocks", Entry: "internal/blocks/kit"}
	if got := bound.Resolve("internal/blocks/kit/sub"); !got.Same(want) {
		t.Errorf("failing: a package under an assembler is %+v, want %+v", got, want)
	}

	if got := bound.Guidance(want); !strings.HasPrefix(got, "Unclassified: it lies beneath assembler internal/blocks/kit of star internal/blocks") {
		t.Errorf("guidance %q", got)
	}

	if notes := bound.Notes(); !slices.ContainsFunc(notes, func(note string) bool {
		return strings.HasPrefix(note, "internal/blocks/kit/sub lies beneath assembler internal/blocks/kit of star internal/blocks")
	}) {
		t.Errorf("no note on the nested package: %q", notes)
	}

	// Passing: listed, in any role, it has that role.
	stars.Stars[1].Engines = []string{"internal/blocks/kit/sub"}
	if err := stars.Validate(); err != nil {
		t.Fatal(err)
	}

	listed, err := stars.Bind(paths)
	if err != nil {
		t.Fatal(err)
	}

	if got := listed.Resolve("internal/blocks/kit/sub"); !got.Same(Placement{Layer: graph.LayerEngine, Star: "internal/blocks", Entry: "internal/blocks/kit/sub"}) {
		t.Errorf("passing: a listed nested package is %+v", got)
	}
}

func TestSnowflakeCheck(t *testing.T) {
	guarded, nested := guard(t), deep(t)
	for _, c := range []struct {
		architecture Architecture
		from, to     string
		rule         string
	}{
		// A leaf uses itself, its star's contracts and engines, and the
		// contracts of every star around it.
		{guarded, "internal/web/fetch", "internal/web", ""},
		{guarded, "internal/blocks/www", "internal/blocks/contracts", ""},
		{guarded, "internal/plan", "internal/executor", ""},
		{guarded, "internal/plan", "internal/contracts/grades", ""},
		{guarded, "internal/blocks/www", "internal/contracts", ""},
		{nested, "internal/gate/models/tiny", "internal/contracts", ""},
		{nested, "internal/gate/models/tiny", "internal/gate/contracts", ""},
		{nested, "internal/gate/tagger", "internal/gate/score", ""},
		{nested, "internal/gate/models/tiny", "internal/gate/models/run", ""},
		// A spoke never uses an assembler: not its own star's, nor any other.
		{guarded, "internal/plan", "internal/assembly", "leaf internal/plan may not depend on assembler internal/assembly: a spoke never uses an assembler: an assembler hands a spoke what it needs; declare a port in the contracts of star . (internal/contracts and internal/contracts/grades), implement it in internal/assembly, and let an assembler of star . hand it in"},
		{guarded, "internal/blocks/www", "internal/blocks/kit", "leaf internal/blocks/www may not depend on assembler internal/blocks/kit: a spoke never uses an assembler"},
		{guarded, "internal/blocks/www", ".", "leaf internal/blocks/www may not depend on assembler .: a spoke never uses an assembler"},
		{guarded, "internal/plan", "internal/gate/pipeline", "leaf internal/plan may not depend on assembler internal/gate/pipeline: a spoke never uses an assembler"},
		// A spoke uses its own star's engines, never those around it.
		{nested, "internal/gate/models/tiny", "internal/gate/score", "leaf internal/gate/models/tiny may not depend on engine internal/gate/score: a spoke uses the engines of its own star, internal/gate/models, and only the contracts of the stars around it; declare a port in the contracts of star internal/gate (internal/gate/contracts), implement it in internal/gate/score, and let the assemblers of stars internal/gate and internal/gate/models hand it in"},
		{guarded, "internal/blocks/summarize", "internal/executor", "a spoke uses the engines of its own star, internal/blocks, and only the contracts of the stars around it"},
		// No dependency runs sideways: spoke to spoke, nor into a branch.
		{guarded, "internal/blocks/cleanse", "internal/blocks/www", "leaf internal/blocks/cleanse may not depend on leaf internal/blocks/www: internal/blocks/cleanse and internal/blocks/www are sibling spokes of star internal/blocks, and no dependency runs sideways; declare a port in the contracts of star internal/blocks (internal/blocks/contracts), implement it in internal/blocks/www, and let an assembler of star internal/blocks hand it in"},
		{guarded, "internal/plan", "internal/web", "internal/plan and internal/web are sibling spokes of star ., and no dependency runs sideways"},
		{guarded, "internal/plan", "internal/blocks/contracts", "leaf internal/plan may not depend on contracts internal/blocks/contracts: internal/plan and internal/blocks are sibling spokes of star ., and no dependency runs sideways, not even on a branch's core; declare what it needs in the contracts of star . (internal/contracts and internal/contracts/grades)"},
		{nested, "internal/gate/tagger", "internal/gate/models/contracts", "not even on a branch's core"},
		{nested, "internal/gate/tagger", "internal/gate/models/tiny", "internal/gate/tagger and internal/gate/models are sibling spokes of star internal/gate"},
		{guarded, "internal/blocks/www", "internal/web", "leaf internal/blocks/www may not depend on leaf internal/web: internal/blocks and internal/web are sibling spokes of star ., and no dependency runs sideways; declare a port in the contracts of star . (internal/contracts and internal/contracts/grades), implement it in internal/web, and let the assemblers of stars . and internal/blocks hand it in"},
		{guarded, "internal/blocks/www", "internal/gate/contracts", "internal/blocks and internal/gate are sibling spokes of star ., and no dependency runs sideways, not even on a branch's core"},

		// Contracts use only contracts of their own star and its ancestors.
		{guarded, "internal/contracts", "internal/contracts/grades", ""},
		{guarded, "internal/blocks/contracts", "internal/contracts", ""},
		{nested, "internal/gate/models/contracts", "internal/contracts", ""},
		{guarded, "internal/contracts", "internal/executor", "contracts internal/contracts may not depend on engine internal/executor: contracts use only contracts of their own star and its ancestors; declare what it needs in contracts, and let internal/executor implement it"},
		{guarded, "internal/contracts", ".", "contracts use only contracts of their own star and its ancestors"},
		{guarded, "internal/contracts", "internal/web", "contracts internal/contracts may not depend on leaf internal/web: contracts use only contracts of their own star and its ancestors; declare what it needs in contracts, and let an assembler hand internal/web in"},
		// Not even a child branch's contracts any more.
		{guarded, "internal/contracts", "internal/blocks/contracts", "contracts internal/contracts may not depend on contracts internal/blocks/contracts: contracts use only contracts of their own star and its ancestors; move what it needs into the contracts of star . (internal/contracts and internal/contracts/grades), which both may use"},
		{guarded, "internal/blocks/contracts", "internal/gate/contracts", "contracts use only contracts of their own star and its ancestors; move what it needs into the contracts of star ."},
		{guarded, "internal/gate/contracts", "internal/executor", "contracts use only contracts of their own star and its ancestors"},

		// An engine uses contracts, its own and its ancestors', and its own
		// star's engines.
		{guarded, "internal/executor", "internal/contracts", ""},
		{nested, "internal/gate/score", "internal/contracts", ""},
		{nested, "internal/gate/models/run", "internal/gate/contracts", ""},
		{guarded, "internal/executor", "internal/web", "engine internal/executor may not depend on leaf internal/web: an engine runs policy over contracts only: it uses the contracts of its own star and its ancestors and the engines of its own star; declare a port in the contracts of star . (internal/contracts and internal/contracts/grades), implement it in internal/web, and let an assembler of star . hand it in"},
		{guarded, "internal/executor", "internal/assembly", "an engine runs policy over contracts only"},
		{nested, "internal/gate/score", "internal/executor", "an engine runs policy over contracts only"},
		{nested, "internal/gate/models/run", "internal/gate/score", "an engine runs policy over contracts only"},
		{guarded, "internal/executor", "internal/blocks/contracts", "an engine runs policy over contracts only"},

		// An assembler wires: its star's core and spokes, its branches'
		// cores and the contracts of its ancestors.
		{guarded, "internal/assembly", "internal/executor", ""},
		{guarded, "internal/assembly", "internal/plan", ""},
		{guarded, "cmd/guardprobe", ".", ""},
		{guarded, "internal/blocks/kit", "internal/blocks/www", ""},
		{guarded, "internal/blocks/kit", "internal/contracts", ""},
		{guarded, "internal/assembly", "internal/blocks/kit", ""},
		{guarded, "internal/assembly", "internal/gate/contracts", ""},
		{nested, "internal/gate/pipeline", "internal/gate/models/assembly", ""},
		{nested, "internal/gate/pipeline", "internal/gate/models/run", ""},
		{nested, "internal/gate/models/assembly", "internal/contracts", ""},
		// Never a branch's spokes, nor anything two stars in.
		{guarded, "internal/assembly", "internal/blocks/www", "assembler internal/assembly may not depend on leaf internal/blocks/www: an assembler reaches a child branch only through its core, and internal/blocks/www is a spoke of branch internal/blocks; let an assembler of star internal/blocks (internal/blocks/kit) build it, and use that"},
		{nested, "internal/assembly", "internal/gate/models/contracts", "assembler internal/assembly may not depend on contracts internal/gate/models/contracts: an assembler reaches only the cores of its child branches, one star in; let the core of branch internal/gate stand for it"},
		// Never past the contracts of the stars around it.
		{guarded, "internal/blocks/kit", "internal/executor", "assembler internal/blocks/kit may not depend on engine internal/executor: an assembler uses only the contracts of the stars around its own; declare a port in the contracts of star . (internal/contracts and internal/contracts/grades), implement it in internal/executor, and let an assembler of star . hand it in"},
		{nested, "internal/gate/models/assembly", "internal/assembly", "an assembler uses only the contracts of the stars around its own"},
		// Nor sideways.
		{guarded, "internal/blocks/kit", "internal/gate/pipeline", "assembler internal/blocks/kit may not depend on assembler internal/gate/pipeline: internal/blocks and internal/gate are sibling spokes of star ., and no dependency runs sideways, not even core to core; declare a port in the contracts of star . (internal/contracts and internal/contracts/grades), implement it in internal/gate, and let an assembler of star . hand it in"},
		{guarded, "internal/blocks/kit", "internal/web", "internal/blocks and internal/web are sibling spokes of star ., and no dependency runs sideways"},
	} {
		allowed, rule := c.architecture.Check(c.architecture.Resolve(c.from), c.architecture.Resolve(c.to))
		if allowed != (rule == "") || (c.rule == "") != (rule == "") || !strings.HasPrefix(rule, c.rule) && !strings.Contains(rule, c.rule) {
			t.Errorf("Check(%s, %s) = %t, %q\nwant %q", c.from, c.to, allowed, rule, c.rule)
		}
	}

	// A star whose core is empty gets new contracts and a new assembler in
	// the advice.
	empty := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Assemblers: []string{"app"}}, {Dir: "lib"}}}
	for _, c := range []struct{ from, to, rule string }{
		{"lib/a", "lib/b", "leaf lib/a may not depend on leaf lib/b: lib/a and lib/b are sibling spokes of star lib, and no dependency runs sideways; declare a port in new contracts of star lib, such as lib/contracts, implement it in lib/b, and let an assembler of star lib hand it in"},
		{"lib/a", "app", "leaf lib/a may not depend on assembler app: a spoke never uses an assembler: an assembler hands a spoke what it needs; declare a port in new contracts of star ., such as contracts, implement it in app, and let the assemblers of stars . and lib hand it in"},
		{"app", "lib/a", "assembler app may not depend on leaf lib/a: an assembler reaches a child branch only through its core, and lib/a is a spoke of branch lib; let a new assembler of star lib, such as lib/assembly, build it, and use that"},
	} {
		if allowed, rule := empty.Check(empty.Resolve(c.from), empty.Resolve(c.to)); allowed || rule != c.rule {
			t.Errorf("Check(%s, %s) = %t, %q\nwant %q", c.from, c.to, allowed, rule, c.rule)
		}
	}
}

// TestSnowflakeLaundering shows the hole the third snowflake closes: under
// the second, a leaf could reach a sibling leaf through an orchestrator
// that re-exported it. Now no spoke uses an assembler, so the alias does
// not help, while the leaf may still use an engine.
func TestSnowflakeLaundering(t *testing.T) {
	architecture := guard(t)
	if allowed, _ := architecture.Check(architecture.Resolve("internal/plan"), architecture.Resolve("internal/assembly")); allowed {
		t.Error("failing: a leaf reaches a sibling through an assembler")
	}

	if allowed, rule := architecture.Check(architecture.Resolve("internal/plan"), architecture.Resolve("internal/executor")); !allowed {
		t.Errorf("passing: a leaf uses its star's engine: %s", rule)
	}
}

func TestSnowflakeBreak(t *testing.T) {
	architecture := deep(t)
	for _, c := range []struct{ from, to, kind string }{
		{"internal/blocks/www", "internal/blocks/contracts", ""},
		{"internal/blocks/www", "internal/contracts", ""},
		{"internal/plan", "internal/web", BreakSideways},
		{"internal/blocks/kit", "internal/gate/pipeline", BreakSideways},
		{"internal/blocks/www", "internal/executor", BreakOut},
		{"internal/blocks/kit", "internal/executor", BreakOut},
		{"internal/assembly", "internal/blocks/www", BreakIn},
		{"internal/plan", "internal/assembly", BreakAssembler},
		{"internal/executor", "internal/web", BreakEngine},
		{"internal/contracts", "internal/gate/pipeline", BreakContracts},
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
	uses("internal/gate/tagger", "internal/gate/pipeline")
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
		{"go:internal/contracts", "go:internal/web"},
		{"go:internal/gate/tagger", "go:internal/gate/pipeline"},
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

func TestSnowflakeValidate(t *testing.T) {
	root := func(contracts, assemblers []string, more ...Star) Architecture {
		return Architecture{Style: StyleSnowflake, Stars: append([]Star{{Dir: ".", Contracts: contracts, Assemblers: assemblers}}, more...)}
	}

	for _, c := range []struct {
		name         string
		architecture Architecture
		says         string
	}{
		{"composition", Architecture{Style: StyleSnowflake, Composition: []string{"cmd/**"}, Stars: []Star{{Dir: "."}}}, "composition does not apply to the snowflake architecture: assemblers do its work, so list main packages, factories and the public API among the assemblers of their star"},
		{"layers", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}, Layers: []Layer{{Name: "web"}}}, "layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share"},
		{"shared", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}, Shared: []string{"lib/**"}}, "layers, shared, boundaries and public do not apply"},
		{"no stars", Architecture{Style: StyleSnowflake}, `the snowflake architecture needs its stars: declare the repository, ".", with its contracts, assemblers and engines, such as .: {contracts: ["internal/contracts"], assemblers: ["."]}`},
		{"a glob for a star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}, {Dir: "internal/*"}}}, `star "internal/*" must be a directory path relative to the repository root, not a glob`},
		{"a star above the root", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "../other"}}}, `star "../other" must be a directory path relative to the repository root, not a glob`},
		{"an absolute star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "/internal"}}}, `star "/internal" must be a directory path relative`},
		{"a dotted star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "./internal"}}}, `star "./internal" must be a directory path relative`},
		{"a star listed twice", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "."}, {Dir: "internal"}, {Dir: "internal"}}}, "star internal is listed twice"},
		{"no root", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal/gate"}, {Dir: "internal/blocks"}, {Dir: "internal/blocks/x"}}}, `the snowflake's root star is the repository itself: declare "." under stars, around internal/blocks and internal/gate`},
		{"an unclean entry", root([]string{"./contracts"}, nil), `contracts entry "./contracts" of star . must be a directory path relative to the repository root, with no empty, . or .. segment`},
		{"an entry with a trailing slash", root([]string{"contracts/"}, nil), `contracts entry "contracts/" of star . must be a directory path relative`},
		{"an entry that climbs out", root(nil, nil, Star{Dir: "internal", Contracts: []string{"internal/../contracts"}}), `contracts entry "internal/../contracts" of star internal must be a directory path relative`},
		{"a glob entry", root(nil, []string{"cmd/*"}), `assemblers entry "cmd/*" of star . is a glob; name each package by its directory, so a new directory stays a spoke until treaty.yaml names it`},
		{"a glob engine", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Engines: []string{"run/**"}}}}, `engines entry "run/**" of star . is a glob`},
		{"a recursive glob entry", root([]string{"internal/core/**"}, nil), `contracts entry "internal/core/**" of star . is a glob`},
		{"an entry outside its star", root(nil, nil, Star{Dir: "internal", Contracts: []string{"pkg/contracts"}}), `contracts entry "pkg/contracts" of star internal lies outside internal; a star's core is inside its directory`},
		{"an entry inside a branch", root([]string{"internal/blocks/contracts"}, nil, Star{Dir: "internal/blocks"}), `contracts entry "internal/blocks/contracts" of star . lies inside branch internal/blocks, whose core is its own; list it under internal/blocks`},
		{"an entry that is a branch", root(nil, []string{"internal/blocks"}, Star{Dir: "internal/blocks"}), `assemblers entry "internal/blocks" of star . lies inside branch internal/blocks`},
		{"an entry holding a star", root(nil, []string{"internal"}, Star{Dir: "internal/blocks"}), `assemblers entry "internal" of star . holds star internal/blocks; a core package is one package and those nested in it, never a star`},
		{"an entry listed twice", root(nil, []string{"internal/executor", "internal/executor"}), `assemblers entry "internal/executor" of star . is listed twice`},
		{"an entry in two lists", root([]string{"internal/executor"}, []string{"internal/executor"}), `"internal/executor" is listed under both the contracts and the assemblers of star .; a core package has one role`},
		{"an assembler and an engine", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Assemblers: []string{"run"}, Engines: []string{"run"}}}}, `"run" is listed under both the assemblers and the engines of star .; a core package has one role`},
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
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Engines: []string{"."}}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Assemblers: []string{"."}}, {Dir: "internal/billing"}, {Dir: "internal/orders", Contracts: []string{"internal/orders/ports"}, Assemblers: []string{"internal/orders"}}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Contracts: []string{"internal/web/port"}}}},
		// Each entry names one package, so one may lie beneath another, in
		// any role.
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Contracts: []string{"internal/contracts", "internal/contracts/grades"}, Engines: []string{"internal/contracts/run"}}}},
		// A file-routed directory's brackets are part of its name.
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Assemblers: []string{"app/[slug]"}}, {Dir: "app/[lang]"}}},
	} {
		if err := valid.Validate(); err != nil {
			t.Errorf("%+v: %v", valid.Stars, err)
		}
	}
}

func TestSnowflakeBind(t *testing.T) {
	port, container := guardStars(), guardStars()
	port.Stars[0].Contracts = append(port.Stars[0].Contracts, "internal/web/port")
	container.Stars[0].Assemblers = append(container.Stars[0].Assemblers, "cmd")
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
			"contracts entry internal/web/port of star . lies inside spoke internal/web: a core package is a child of its star or of another core package, never part of a spoke; move the package out of internal/web, or declare internal/web a star with internal/web/port in its contracts",
			"contracts entry internal/web/port of star . lies inside spoke internal/web"},
		{"an entry naming a container", container, append(slices.Clone(guardPaths), "cmd/a", "cmd/b", "cmd/b/x"),
			"assemblers entry cmd of star . holds no package of its own, only cmd/a, cmd/b and cmd/guardprobe below it; name each package, so a new one stays a spoke until treaty.yaml names it",
			"assemblers entry cmd of star . holds no package of its own"},
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

	// A container entry claims only itself: what lies under it is
	// unclassified, beneath it.
	bound, _ := container.Bind(append(slices.Clone(guardPaths), "cmd/a"))
	if got := bound.Resolve("cmd/a"); !got.Same(Placement{Layer: graph.LayerUnclassified, Star: ".", Entry: "cmd"}) {
		t.Errorf("a container entry: %+v", got)
	}

	// An entry beneath another lies in no spoke.
	under := guardStars()
	under.Stars[1].Engines = []string{"internal/blocks/kit/sub"}
	if _, err := under.Bind(append(slices.Clone(guardPaths), "internal/blocks/kit/sub")); err != nil {
		t.Errorf("an entry beneath an entry: %v", err)
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
		{".", "Assembler . of star .: wires its star. It may use the core and the spokes of star . and the cores of its branches internal/blocks and internal/gate, never a branch's spokes; no spoke may use it.",
			[]string{"contracts internal/contracts", "contracts internal/contracts/grades", "assembler .", "assembler cmd/guardprobe", "assembler internal/assembly", "engine internal/executor", "the leaves of star .", "contracts internal/blocks/contracts", "assembler internal/blocks/kit", "contracts internal/gate/contracts", "assembler internal/gate/pipeline"}},
		{"internal/blocks/kit", "Assembler internal/blocks/kit of star internal/blocks, a branch of .: wires its star. It may use the core and the spokes of star internal/blocks and the contracts of the star around it, ., nothing else; no spoke may use it.",
			[]string{"contracts internal/blocks/contracts", "assembler internal/blocks/kit", "the leaves of star internal/blocks", "contracts internal/contracts", "contracts internal/contracts/grades"}},
		{"internal/executor", "Engine internal/executor of star .: runs policy over contracts. It may use the contracts of star . and the engines of star ., never a spoke or an assembler.",
			[]string{"contracts internal/contracts", "contracts internal/contracts/grades", "engine internal/executor"}},
		{"internal/contracts", "Contracts internal/contracts of star .: interfaces, value types and their pure methods, errors and constants, importing only the standard library and declaring no package-level variable but sentinel errors. They may use the contracts of star ., never a spoke, an engine or an assembler.",
			[]string{"contracts internal/contracts", "contracts internal/contracts/grades"}},
		{"internal/blocks/contracts", "Contracts internal/blocks/contracts of star internal/blocks, a branch of .: interfaces, value types and their pure methods, errors and constants, importing only the standard library and declaring no package-level variable but sentinel errors. They may use the contracts of star internal/blocks and of the star around it, ., never a spoke, an engine or an assembler.",
			[]string{"contracts internal/blocks/contracts", "contracts internal/contracts", "contracts internal/contracts/grades"}},
		{"internal/plan", "Leaf internal/plan of star .: may use itself, the contracts and engines of star ., never an assembler or a sibling spoke. A leaf declares the interfaces it consumes; to use a sibling, depend on a port in the contracts of star . and let an assembler of star . hand the sibling in.",
			[]string{"leaf internal/plan", "contracts internal/contracts", "contracts internal/contracts/grades", "engine internal/executor"}},
		{"internal/blocks/www", "Leaf internal/blocks/www of star internal/blocks, a branch of .: may use itself, the contracts and engines of star internal/blocks and the contracts of the star around it, ., never an assembler or a sibling spoke. A leaf declares the interfaces it consumes; to use a sibling, depend on a port in the contracts of star internal/blocks and let an assembler of star internal/blocks hand the sibling in.",
			[]string{"leaf internal/blocks/www", "contracts internal/blocks/contracts", "contracts internal/contracts", "contracts internal/contracts/grades"}},
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
		{Layer: graph.LayerLeaf, Star: ".", Spoke: "app"}:                 "Leaf app of star .: may use only itself, since star . has no contracts or engines yet; list them under stars in treaty.yaml.",
		{Layer: graph.LayerUnclassified, Star: "internal/blocks"}:         "Unclassified: the own package of star internal/blocks holds code, so treaty.yaml must list it in that star's contracts, assemblers or engines; until then its dependencies are not checked.",
		{Layer: graph.LayerUnclassified, Star: ".", Spoke: "internal"}:    "Unclassified: internal holds a package where treaty.yaml's stars look through it, so its dependencies are not checked.",
		{Layer: graph.LayerAssembler, Star: "lib", Entry: "lib/assembly"}: "Assembler lib/assembly of star lib, a branch of .: wires its star. It may use the core and the spokes of star lib and the contracts of the star around it, ., nothing else; no spoke may use it.",
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
	got := architecture.Summary()
	for _, want := range []string{
		"Contracts use only contracts of their own star and its ancestors.",
		"a spoke never uses an assembler.",
		"A leaf declares the interfaces it consumes; spokes use ancestor contracts directly; there are no re-exports.",
		"Stars: . (contracts internal/contracts and internal/contracts/grades; assemblers ., cmd/guardprobe and internal/assembly; engines internal/executor), internal/blocks (branch of .; contracts internal/blocks/contracts; assemblers internal/blocks/kit; no engines), internal/gate (branch of .; contracts internal/gate/contracts; assemblers internal/gate/pipeline; no engines).",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary() = %q\nlacks %q", got, want)
		}
	}

	if got := empty.Summary(); !strings.HasSuffix(got, "Stars: . (no contracts; no assemblers; no engines), lib (branch of .; no contracts; no assemblers; no engines).") {
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
		{"internal/contracts/grades", []Reason{{[]string{"stars", ".", "contracts"}, "internal/contracts/grades"}}},
		{".", []Reason{{[]string{"stars", ".", "assemblers"}, "."}}},
		{"internal/executor", []Reason{{[]string{"stars", ".", "engines"}, "internal/executor"}}},
		{"internal/blocks/kit", []Reason{{[]string{"stars", "internal/blocks", "assemblers"}, "internal/blocks/kit"}}},
		{"internal/web/fetch", []Reason{{[]string{"stars", "."}, ""}}},
		{"internal/blocks/www", []Reason{{[]string{"stars", "internal/blocks"}, ""}}},
		{"internal/gate/pipeline/tinylogos", nil},
		{"internal/blocks", nil},
	} {
		got := architecture.Reasons(c.path)
		if !slices.EqualFunc(got, c.want, func(a, b Reason) bool { return a.Glob == b.Glob && slices.Equal(a.Path, b.Path) }) {
			t.Errorf("%s: got %+v, want %+v", c.path, got, c.want)
		}
	}

	// A star that still says orchestrators is pointed at that key.
	legacy := guardStars()
	legacy.Stars[1].Deprecated = true
	if got := legacy.Reasons("internal/blocks/kit"); len(got) != 1 || !slices.Equal(got[0].Path, []string{"stars", "internal/blocks", "orchestrators"}) {
		t.Errorf("a deprecated star's assembler: %+v", got)
	}
}

func TestSnowflakeTree(t *testing.T) {
	architecture := guard(t)
	slices.Reverse(architecture.Stars)
	want := []StarNode{
		{Dir: ".", Depth: 0, Contracts: []string{"internal/contracts", "internal/contracts/grades"}, Assemblers: []string{".", "cmd/guardprobe", "internal/assembly"}, Engines: []string{"internal/executor"},
			Leaves: []string{"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web"}, Branches: []string{"internal/blocks", "internal/gate"}},
		{Dir: "internal/blocks", Parent: ".", Depth: 1, Contracts: []string{"internal/blocks/contracts"}, Assemblers: []string{"internal/blocks/kit"}, Engines: []string{},
			Leaves: []string{"internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www"}, Branches: []string{}},
		{Dir: "internal/gate", Parent: ".", Depth: 1, Contracts: []string{"internal/gate/contracts"}, Assemblers: []string{"internal/gate/pipeline"}, Engines: []string{},
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
	for _, path := range []string{"internal/blocks", "internal/blocks/contracts", "internal/blocks/kit", "internal/blocks/www", "internal/executor"} {
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
		"new_module go:internal/blocks/kit":       "New module go:internal/blocks/kit: Placed in assembler internal/blocks/kit of star internal/blocks.",
		"new_module go:internal/executor":         "New module go:internal/executor: Placed in engine internal/executor of star ..",
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
	architecture.Stars[1].Deprecated = true
	architecture.Stars[2].Contracts = append(architecture.Stars[2].Contracts, "internal/gate/ports")
	architecture.Stars[2].Assemblers = append(architecture.Stars[2].Assemblers, "internal/gate/run")
	architecture.Stars[2].Engines = append(architecture.Stars[2].Engines, "internal/gate/policy")
	bound, err := architecture.Bind(append(slices.Clone(guardPaths), "internal/blocks", "internal/billing/a", "internal/billing/b"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"star internal/billing has no contracts, assemblers or engines, so nothing wires its spokes and they may use only themselves and the contracts of the stars around it",
		"treaty.yaml declares star internal/blcoks under stars, but no module lies in it; check the directory's spelling",
		"star internal/blocks lists orchestrators, the name the second snowflake gave its assemblers; rename the key to assemblers, and move each package that only runs policy over contracts to engines",
		"star internal/blocks holds a package of its own, internal/blocks, which treaty.yaml lists in none of its contracts, assemblers or engines, so it is unclassified; list it in one of them",
		"contracts entry internal/gate/ports of star internal/gate matches no module; check its spelling",
		"assemblers entry internal/gate/run of star internal/gate matches no module; check its spelling",
		"engines entry internal/gate/policy of star internal/gate matches no module; check its spelling",
		"internal/gate/pipeline/tinylogos lies beneath assembler internal/gate/pipeline of star internal/gate, and a core role covers only the package an entry names, so it is unclassified; list it under the contracts, assemblers or engines of star internal/gate, or move it out of internal/gate/pipeline",
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
	root := "the contracts of star . (internal/contracts and internal/contracts/grades)"
	for _, c := range []struct{ from, to, declare, hands string }{
		{"internal/plan", "internal/web", root, "an assembler of star ."},
		{"internal/blocks/www", "internal/web", root, "the assemblers of stars . and internal/blocks"},
		{"internal/blocks/summarize", "internal/executor", root, "the assemblers of stars . and internal/blocks"},
		{"internal/blocks/kit", "internal/gate/pipeline", root, "an assembler of star ."},
		{"internal/gate/models/tiny", "internal/web", root, "the assemblers of stars ., internal/gate and internal/gate/models"},
		{"internal/gate/score", "internal/gate/tagger", "the contracts of star internal/gate (internal/gate/contracts)", "an assembler of star internal/gate"},
	} {
		declare, hands := architecture.Handover(architecture.Resolve(c.from), architecture.Resolve(c.to))
		if declare != c.declare || hands != c.hands {
			t.Errorf("Handover(%s, %s) = %q, %q", c.from, c.to, declare, hands)
		}
	}
}

func TestSnowflakeTestImportsAndLinknames(t *testing.T) {
	architecture := guardStars()
	g := graph.New()
	for _, path := range guardPaths {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
	}

	g.TestImports = []graph.Import{
		{From: "go:internal/plan", To: "go:internal/web", File: "internal/plan/plan_test.go", Line: 5, Test: true},
		{From: "go:internal/plan", To: "go:internal/web", File: "internal/plan/x_test.go", Line: 7, Test: true},
		{From: "go:internal/plan", To: "go:internal/contracts", File: "internal/plan/plan_test.go", Line: 6, Test: true},
		// A directory of only tests is checked as its path places it.
		{From: "go:internal/e2e", To: "go:internal/assembly", File: "internal/e2e/e2e_test.go", Line: 3, Test: true},
	}
	g.Linknames = []graph.Linkname{{Module: "go:internal/tracer", Local: "now", Target: "runtime.nanotime", File: "internal/tracer/now.go", Line: 9}}
	g.Normalize()
	architecture.Assign(g)

	var got []string
	for _, violation := range architecture.Violations(g) {
		got = append(got, fmt.Sprintf("%s %s %t %d", violation.From, violation.To, violation.Test, len(violation.Imports)))
	}

	want := []string{"go:internal/e2e go:internal/assembly true 1", "go:internal/plan go:internal/web true 2", "go:internal/tracer linkname runtime.nanotime false 1"}
	if !slices.Equal(got, want) {
		t.Errorf("violations %q, want %q", got, want)
	}

	// rules.tests: skip leaves the tests unchecked, not the linkname.
	architecture.Tests = "skip"
	if violations := architecture.Violations(g); len(violations) != 1 || violations[0].ToLayer != "linkname" {
		t.Errorf("skipped tests: %+v", violations)
	}

	// The base's test violation is existing, apart from the edge's own.
	head := []Violation{{From: "a", To: "b", Test: true}, {From: "a", To: "b"}}
	MarkExisting(head, []Violation{{From: "a", To: "b", Test: true}})
	if !head[0].Existing || head[1].Existing || head[0].Target() != "a -> b (test)" {
		t.Errorf("existing: %+v", head)
	}
}

func TestSnowflakeUnreadAndImpureFindings(t *testing.T) {
	architecture := guardStars()
	build := func() *graph.Graph {
		g := graph.New()
		for _, path := range []string{"internal/contracts", "internal/plan"} {
			g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path, Files: []string{path + "/x.go"}})
		}

		g.Unread = []graph.Import{{From: "go:internal/plan", To: "go:internal/_x", File: "internal/plan/x.go", Line: 3}}
		g.Foreign = []graph.Foreign{
			{From: "go:internal/contracts", Path: "net/http", File: "internal/contracts/x.go", Line: 4, Standard: true},
			{From: "go:internal/contracts", Path: "strings", File: "internal/contracts/x.go", Line: 5, Standard: true},
			{From: "go:internal/plan", Path: "net/http", File: "internal/plan/x.go", Line: 4, Standard: true},
		}
		g.Globals = []graph.Global{
			{Module: "go:internal/contracts", Name: "ErrGone", File: "internal/contracts/x.go", Line: 8, Sentinel: true},
			{Module: "go:internal/contracts", Name: "_", File: "internal/contracts/x.go", Line: 9},
			{Module: "go:internal/contracts", Name: "Cache", File: "internal/contracts/x.go", Line: 10},
		}
		g.Normalize()
		architecture.Assign(g)
		return g
	}

	head, base := build(), build()
	base.Globals = base.Globals[:2]
	got := map[string]bool{}
	for _, finding := range Rank(head, base, nil, nil) {
		if finding.Kind == FindingImpure || finding.Kind == FindingUnclassified {
			got[finding.Kind+" "+finding.Target] = finding.Existing
		}
	}

	want := map[string]bool{
		"unclassified go:internal/_x imported by go:internal/plan": true,
		"impure_contracts go:internal/contracts: import net/http":  true,
		"impure_contracts go:internal/contracts: var Cache":        false,
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings %v, want %v", got, want)
	}
}
