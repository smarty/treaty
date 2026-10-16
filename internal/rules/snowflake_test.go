package rules

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/graph"
)

// snowflake is agentportal-guard after its reorganisation: a root star
// internal with branches blocks and gate.
func snowflake() Architecture {
	return Architecture{Style: StyleSnowflake, Composition: []string{".", "cmd/**", "internal/factory/**"}, Stars: []Star{
		{Dir: "internal", Core: []string{"internal/core/**"}},
		{Dir: "internal/blocks", Core: []string{"internal/blocks/core/**"}},
		{Dir: "internal/gate", Core: []string{"internal/gate/core/**"}},
	}}
}

func TestSnowflakeResolve(t *testing.T) {
	architecture := snowflake()
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	exact := snowflake()
	exact.Composition = append(exact.Composition, "internal/blocks")
	exact.Stars[0].Core = append(exact.Stars[0].Core, "internal/web/port")
	if err := exact.Validate(); err != nil {
		t.Fatal(err)
	}

	rooted := Architecture{Style: StyleSnowflake, Composition: []string{"cmd/**"}, Stars: []Star{{Dir: "."}, {Dir: "internal", Core: []string{"internal/core/**"}}}}
	if err := rooted.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		architecture Architecture
		path         string
		want         Placement
	}{
		{architecture, ".", Placement{Layer: graph.LayerComposition}},
		{architecture, "cmd/guardprobe", Placement{Layer: graph.LayerComposition}},
		{architecture, "internal/factory", Placement{Layer: graph.LayerComposition}},
		{architecture, "internal", Placement{Layer: graph.LayerCore, Star: "internal"}},
		{architecture, "internal/core", Placement{Layer: graph.LayerCore, Star: "internal"}},
		{architecture, "internal/core/trust", Placement{Layer: graph.LayerCore, Star: "internal"}},
		{architecture, "internal/plan", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/plan"}},
		{architecture, "internal/web/fetch", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/web"}},
		{architecture, "internal/blocks", Placement{Layer: graph.LayerCore, Star: "internal/blocks"}},
		{architecture, "internal/blocks/core/model", Placement{Layer: graph.LayerCore, Star: "internal/blocks"}},
		{architecture, "internal/blocks/www/sub", Placement{Layer: graph.LayerSpoke, Star: "internal/blocks", Spoke: "internal/blocks/www"}},
		{architecture, "internal/gate/presence", Placement{Layer: graph.LayerSpoke, Star: "internal/gate", Spoke: "internal/gate/presence"}},
		// A directory nobody listed is a leaf the moment it holds code.
		{architecture, "internal/patterns", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/patterns"}},
		// The innermost star's globs alone apply: internal/core/** of
		// internal never reaches a path inside a branch.
		{architecture, "internal/gate/core", Placement{Layer: graph.LayerCore, Star: "internal/gate"}},
		{architecture, "internal/blocksx", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/blocksx"}},
		{architecture, "scripts/x", Placement{Layer: graph.LayerUnclassified}},
		// An exact core path beats the spoke its directory would give.
		{exact, "internal/web/port", Placement{Layer: graph.LayerCore, Star: "internal"}},
		{exact, "internal/web", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/web"}},
		// An exact composition path beats a star's own package.
		{exact, "internal/blocks", Placement{Layer: graph.LayerComposition}},
		// A composition glob beats a spoke.
		{architecture, "internal/factory/wire", Placement{Layer: graph.LayerComposition}},
		{rooted, ".", Placement{Layer: graph.LayerCore, Star: "."}},
		{rooted, "scripts/x", Placement{Layer: graph.LayerSpoke, Star: ".", Spoke: "scripts"}},
		{rooted, "internal/plan", Placement{Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/plan"}},
		{rooted, "cmd/tool", Placement{Layer: graph.LayerComposition}},
	} {
		if got := c.architecture.Resolve(c.path); !got.Same(c.want) {
			t.Errorf("Resolve(%s) = %+v, want %+v", c.path, got, c.want)
		}
	}
}

func TestSnowflakeCheck(t *testing.T) {
	architecture := snowflake()
	for _, c := range []struct {
		from, to string
		rule     string
	}{
		// 1. Composition may use anything.
		{"cmd/guardprobe", "internal/plan", ""},
		{"internal/factory", "internal/blocks/www", ""},
		// 2. Nothing may use composition.
		{"internal/web", "internal/factory", "spoke internal/web may not depend on composition: nothing may depend on the composition root"},
		{"internal/blocks/core", ".", "core of star internal/blocks may not depend on composition: nothing may depend on the composition root"},
		// 3. Unclassified ends are not checked.
		{"scripts/x", "internal/plan", ""},
		{"internal/plan", "scripts/x", ""},
		// 4. A core region uses itself freely.
		{"internal", "internal/core/trust", ""},
		{"internal/core/trust", "internal", ""},
		// 5. A core uses the cores of the stars around it.
		{"internal/blocks/core", "internal/core", ""},
		{"internal/blocks", "internal", ""},
		// 6. A core may not reach into its own leaves.
		{"internal/core", "internal/web", "core of star internal may not depend on spoke internal/web: internal/web is a spoke of star internal, and a core may use only itself and the cores of the stars around it; declare a port in the core of star internal, and let composition hand internal/web in"},
		{"internal/blocks", "internal/blocks/www/sub", "core of star internal/blocks may not depend on spoke internal/blocks/www: internal/blocks/www is a spoke of star internal/blocks, and a core may use only itself and the cores of the stars around it; declare a port in the core of star internal/blocks, and let composition hand internal/blocks/www in"},
		// 7. Nor into anything in its branches.
		{"internal/core", "internal/gate/core", "core of star internal may not depend on core of star internal/gate: internal/gate is a spoke of star internal, and a core may use only itself and the cores of the stars around it; declare a port in the core of star internal, and let composition hand internal/gate in"},
		{"internal/core", "internal/blocks/www", "core of star internal may not depend on spoke internal/blocks/www: internal/blocks is a spoke of star internal, and a core may use only itself and the cores of the stars around it; declare a port in the core of star internal, and let composition hand internal/blocks in"},
		// 8. A branch's core may not use a cousin's core or an uncle leaf.
		{"internal/blocks/core", "internal/web", "core of star internal/blocks may not depend on spoke internal/web: internal/blocks and internal/web are sibling spokes of star internal, and spokes never depend sideways; declare a port in the core of star internal, implement it in internal/web, and let composition hand it in"},
		{"internal/blocks/core", "internal/gate/core", "core of star internal/blocks may not depend on core of star internal/gate: internal/blocks and internal/gate are sibling spokes of star internal, and spokes never depend sideways, not even through a branch's core; declare a port in the core of star internal, implement it in internal/gate, and let composition hand it in"},
		// 9. A leaf uses itself freely.
		{"internal/web/fetch", "internal/web", ""},
		// 10. A leaf uses its own star's core.
		{"internal/blocks/cleanse", "internal/blocks/core", ""},
		{"internal/blocks/www", "internal/blocks", ""},
		// 11. And the cores of every star around it.
		{"internal/blocks/cleanse", "internal/core", ""},
		{"internal/gate/tagger", "internal", ""},
		// 12. Never a sibling leaf.
		{"internal/plan", "internal/web", "spoke internal/plan may not depend on spoke internal/web: internal/plan and internal/web are sibling spokes of star internal, and spokes never depend sideways; declare a port in the core of star internal, implement it in internal/web, and let composition hand it in"},
		{"internal/blocks/cleanse", "internal/blocks/www", "spoke internal/blocks/cleanse may not depend on spoke internal/blocks/www: internal/blocks/cleanse and internal/blocks/www are sibling spokes of star internal/blocks, and spokes never depend sideways; declare a port in the core of star internal/blocks, implement it in internal/blocks/www, and let composition hand it in"},
		// 13. Never a sibling branch, not even its core.
		{"internal/plan", "internal/blocks/core", "spoke internal/plan may not depend on core of star internal/blocks: internal/plan and internal/blocks are sibling spokes of star internal, and spokes never depend sideways, not even through a branch's core; declare a port in the core of star internal, implement it in internal/blocks, and let composition hand it in"},
		{"internal/plan", "internal/blocks/www", "spoke internal/plan may not depend on spoke internal/blocks/www: internal/plan and internal/blocks are sibling spokes of star internal, and spokes never depend sideways; declare a port in the core of star internal, implement it in internal/blocks, and let composition hand it in"},
		// 14. Nothing in one child of a star uses another child.
		{"internal/gate/tagger", "internal/anthropic", "spoke internal/gate/tagger may not depend on spoke internal/anthropic: internal/gate and internal/anthropic are sibling spokes of star internal, and spokes never depend sideways; declare a port in the core of star internal, implement it in internal/anthropic, and let composition hand it in"},
		{"internal/gate/tagger", "internal/blocks/core", "spoke internal/gate/tagger may not depend on core of star internal/blocks: internal/gate and internal/blocks are sibling spokes of star internal, and spokes never depend sideways, not even through a branch's core; declare a port in the core of star internal, implement it in internal/blocks, and let composition hand it in"},
	} {
		allowed, rule := architecture.Check(architecture.Resolve(c.from), architecture.Resolve(c.to))
		if allowed != (rule == "") || rule != c.rule {
			t.Errorf("Check(%s, %s) = %t, %q\nwant %q", c.from, c.to, allowed, rule, c.rule)
		}
	}
}

func TestSnowflakeViolations(t *testing.T) {
	architecture := snowflake()
	g := graph.New()
	for _, path := range []string{".", "cmd/guardprobe", "internal/factory", "internal", "internal/core", "internal/plan", "internal/web", "internal/blocks", "internal/blocks/core", "internal/blocks/cleanse", "internal/blocks/www", "internal/gate", "internal/gate/core", "internal/gate/tagger"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	uses("internal/plan", "internal/web")
	uses("internal/blocks/www", "internal/gate/core")
	uses("internal/core", "internal/web")
	uses("internal/web", "internal/factory")
	uses("internal/web", "internal/core")
	uses("internal/blocks/cleanse", "internal/blocks/core")
	uses("internal/blocks/core", "internal/core")
	uses("internal/gate/tagger", "internal/gate/core")
	for _, path := range []string{"internal", "internal/plan", "internal/web", "internal/blocks", "internal/blocks/www", "internal/gate/tagger"} {
		uses("internal/factory", path)
	}

	uses("cmd/guardprobe", "internal/factory")
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
		{"go:internal/blocks/www", "go:internal/gate/core"},
		{"go:internal/core", "go:internal/web"},
		{"go:internal/plan", "go:internal/web"},
		{"go:internal/web", "go:internal/factory"},
	}

	if !slices.Equal(got, want) {
		t.Errorf("violations %v, want %v", got, want)
	}

	for _, module := range g.Modules {
		if want := architecture.Resolve(module.Path); !placementOf(module).Same(want) {
			t.Errorf("%s: Assign gave %+v, Resolve %+v", module.Path, placementOf(module), want)
		}
	}
}

func TestSnowflakeValidate(t *testing.T) {
	for _, c := range []struct {
		name         string
		architecture Architecture
		says         string
	}{
		{"no stars", Architecture{Style: StyleSnowflake}, `the snowflake architecture needs at least one star; name its directory and its core under stars, such as internal: ["internal/core/**"]`},
		{"two roots", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal"}, {Dir: "pkg"}}}, `stars internal and pkg are both outermost; a snowflake has one root star, so declare a star around both, such as ".", or nest one inside the other`},
		{"a star that skips a directory", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal"}, {Dir: "internal/features/orders"}}}, "star internal/features/orders is not a spoke of a star: a branch must be a child directory of the star around it; declare internal/features as a star, or move the star"},
		{"a glob for a star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal/*"}}}, `star "internal/*" must be a directory path relative to the repository root, not a glob`},
		{"a star above the root", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "../other"}}}, `star "../other" must be a directory path relative to the repository root, not a glob`},
		{"an absolute star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "/internal"}}}, `star "/internal" must be a directory path relative to the repository root, not a glob`},
		{"a dotted star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "./internal"}}}, `star "./internal" must be a directory path relative to the repository root, not a glob`},
		{"a star with a trailing slash", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal/"}}}, `star "internal/" must be a directory path relative to the repository root, not a glob`},
		{"a star listed twice", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal"}, {Dir: "internal"}}}, "star internal is listed twice"},
		{"a core outside its star", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"pkg/core/**"}}}}, `core glob "pkg/core/**" of star internal lies outside internal; a star's core is inside its directory`},
		{"a core of every child", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/**"}}}}, `core glob "internal/**" of star internal does not name a directory of its own; start it with a child directory of internal, such as internal/core/**`},
		{"a core of any child", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/*"}}}}, `core glob "internal/*" of star internal does not name a directory of its own`},
		{"a core inside any child", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/*/contracts/**"}}}}, `core glob "internal/*/contracts/**" of star internal does not name a directory of its own`},
		{"a root core of every child", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Core: []string{"**/core/**"}}}}, `core glob "**/core/**" of star . does not name a directory of its own; start it with a child directory of ., such as core/**`},
		{"a core inside a branch", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/blocks/core/**"}}, {Dir: "internal/blocks"}}}, `core glob "internal/blocks/core/**" of star internal lies inside branch internal/blocks, whose core is its own; list it under internal/blocks`},
		{"a core that is a branch", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/blocks"}}, {Dir: "internal/blocks"}}}, `core glob "internal/blocks" of star internal lies inside branch internal/blocks`},
		{"layers", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal"}}, Layers: []Layer{{Name: "web"}}}, "layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share"},
		{"shared", Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "internal"}}, Shared: []string{"lib/**"}}, "layers, shared, boundaries and public do not apply"},
		{"stars under hexagonal", Architecture{Style: StyleHexagonal, Stars: []Star{{Dir: "internal"}}}, "stars apply only to the snowflake architecture"},
		{"stars under none", Architecture{Style: StyleNone, Stars: []Star{{Dir: "internal"}}}, "stars apply only to the snowflake architecture"},
	} {
		err := c.architecture.Validate()
		if !errors.Is(err, ErrArchitecture) || !strings.HasPrefix(err.Error(), "invalid architecture: "+c.says) {
			t.Errorf("%s: got %v\nwant %s", c.name, err, c.says)
		}
	}

	for _, valid := range []Architecture{
		snowflake(),
		{Style: StyleSnowflake, Stars: []Star{{Dir: "."}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: ".", Core: []string{"core/**"}}, {Dir: "internal"}}},
		{Style: StyleSnowflake, Composition: []string{"cmd/**"}, Stars: []Star{{Dir: "internal", Core: []string{"internal"}}, {Dir: "internal/billing", Core: []string{}}, {Dir: "internal/orders", Core: []string{"internal/orders/ports/**", "internal/orders/flow"}}}},
		{Style: StyleSnowflake, Stars: []Star{{Dir: "internal", Core: []string{"internal/web/port"}}}},
	} {
		if err := valid.Validate(); err != nil {
			t.Errorf("%+v: %v", valid.Stars, err)
		}
	}
}

func TestSnowflakeGuidanceMayUseSummary(t *testing.T) {
	architecture := snowflake()
	for _, c := range []struct {
		path     string
		guidance string
		mayUse   []string
	}{
		{"internal/core", "Core of star internal: its contracts and orchestrators. It may use only itself, never its own spokes; reach a spoke through a port declared here, which composition hands in.",
			[]string{"core of internal (internal, internal/core/**)"}},
		{"internal/blocks", "Core of star internal/blocks, a branch of internal: its contracts and orchestrators. It may use itself and the core of internal, never its own spokes; reach a spoke through a port declared here, which composition hands in.",
			[]string{"core of internal/blocks (internal/blocks, internal/blocks/core/**)", "core of internal (internal, internal/core/**)"}},
		{"internal/plan", "Spoke internal/plan of star internal: may use itself and the core of internal, never a sibling spoke or branch. To use a sibling, depend on a port in a core and let composition hand the sibling in.",
			[]string{"spoke internal/plan", "core of internal (internal, internal/core/**)"}},
		{"internal/blocks/www", "Spoke internal/blocks/www of star internal/blocks: may use itself and the cores of internal/blocks and internal, never a sibling spoke or branch. To use a sibling, depend on a port in a core and let composition hand the sibling in.",
			[]string{"spoke internal/blocks/www", "core of internal/blocks (internal/blocks, internal/blocks/core/**)", "core of internal (internal, internal/core/**)"}},
		{"scripts/x", "Unclassified: outside the root star internal, so its dependencies are not checked. Add it to composition, or move it inside internal.",
			[]string{"every core and spoke"}},
		{"cmd/guardprobe", "Composition: wires everything together. It may use every module; nothing may use it.",
			[]string{"every core and spoke", "composition"}},
	} {
		placement := architecture.Resolve(c.path)
		if got := architecture.Guidance(placement); got != c.guidance {
			t.Errorf("Guidance(%s) = %q\nwant %q", c.path, got, c.guidance)
		}

		if got := architecture.MayUse(placement); !slices.Equal(got, c.mayUse) {
			t.Errorf("MayUse(%s) = %q\nwant %q", c.path, got, c.mayUse)
		}
	}

	// Stars are listed root first, whatever order treaty.yaml gives.
	slices.Reverse(architecture.Stars)
	want := "Rules (snowflake): the code is a star of stars. Each star has one core, holding its contracts and orchestrators, and spokes, its other child directories; a spoke that is a star of its own is a branch. " +
		"A spoke may use itself, its own star's core and the cores of every star around it. A core may use itself and the cores around it, never its own spokes. " +
		"No spoke may use a sibling spoke or anything in a sibling branch, not even its core: an orchestrator reaches its spokes through ports declared in its core, and composition hands the implementations in. " +
		"Composition may use everything; nothing may use composition. " +
		"Stars: internal (core internal/core/**), internal/blocks (branch of internal, core internal/blocks/core/**), internal/gate (branch of internal, core internal/gate/core/**)."
	if got := architecture.Summary(); got != want {
		t.Errorf("Summary() = %q\nwant %q", got, want)
	}

	if names := architecture.LayerNames(); names != nil {
		t.Errorf("a snowflake has no layer names: %v", names)
	}
}

func TestSnowflakeReasons(t *testing.T) {
	architecture := snowflake()
	architecture.Stars[0].Core = append(architecture.Stars[0].Core, "internal/web/port")
	for _, c := range []struct {
		path string
		want []Reason
	}{
		{"internal/core/trust", []Reason{{[]string{"stars", "internal"}, "internal/core/**"}}},
		{"internal/web/port", []Reason{{[]string{"stars", "internal"}, "internal/web/port"}}},
		{"internal/blocks", []Reason{{[]string{"stars", "internal/blocks"}, ""}}},
		{"internal/blocks/www/sub", []Reason{{[]string{"stars", "internal/blocks"}, ""}}},
		{"cmd/guardprobe", []Reason{{[]string{"composition"}, "cmd/**"}}},
		{"scripts/x", nil},
	} {
		got := architecture.Reasons(c.path)
		if !slices.EqualFunc(got, c.want, func(a, b Reason) bool { return a.Glob == b.Glob && slices.Equal(a.Path, b.Path) }) {
			t.Errorf("%s: got %+v, want %+v", c.path, got, c.want)
		}
	}
}

func TestSnowflakeTree(t *testing.T) {
	architecture := snowflake()
	slices.Reverse(architecture.Stars)
	got := architecture.Tree([]string{".", "cmd/guardprobe", "internal/factory", "internal", "internal/core", "internal/web/fetch", "internal/plan", "internal/web",
		"internal/blocks", "internal/blocks/core", "internal/blocks/www", "internal/blocks/www/sub", "internal/blocks/cleanse", "internal/gate/presence", "scripts/x"})
	want := []StarNode{
		{Dir: "internal", Depth: 0, Core: []string{"internal/core/**"}, Leaves: []string{"internal/plan", "internal/web"}, Branches: []string{"internal/blocks", "internal/gate"}},
		{Dir: "internal/blocks", Parent: "internal", Depth: 1, Core: []string{"internal/blocks/core/**"}, Leaves: []string{"internal/blocks/cleanse", "internal/blocks/www"}, Branches: []string{}},
		{Dir: "internal/gate", Parent: "internal", Depth: 1, Core: []string{"internal/gate/core/**"}, Leaves: []string{"internal/gate/presence"}, Branches: []string{}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tree = %+v\nwant %+v", got, want)
	}

	// Each star comes before the stars inside it, even when a sibling's
	// name sorts between them.
	deep := Architecture{Style: StyleSnowflake, Stars: []Star{{Dir: "a-b"}, {Dir: "a/b"}, {Dir: "a"}, {Dir: "."}}}
	var dirs []string
	for _, node := range deep.Tree(nil) {
		dirs = append(dirs, node.Dir)
	}

	if !slices.Equal(dirs, []string{".", "a", "a/b", "a-b"}) {
		t.Errorf("order %v", dirs)
	}

	if (Architecture{Style: StyleLayered}).Tree([]string{"x"}) != nil {
		t.Error("only a snowflake has a star tree")
	}
}

func TestSnowflakeFindings(t *testing.T) {
	architecture := snowflake()
	base, head := graph.New(), graph.New()
	for _, path := range []string{"internal/blocks", "internal/blocks/www", "scripts/x"} {
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
		"new_module go:internal/blocks":     "New module go:internal/blocks: Placed in the core of star internal/blocks.",
		"new_module go:internal/blocks/www": "New module go:internal/blocks/www: Placed in spoke internal/blocks/www of star internal/blocks.",
		"unclassified go:scripts/x":         "go:scripts/x is unclassified: treaty.yaml places this module nowhere, so its dependencies are not checked.",
	} {
		if details[key] != want {
			t.Errorf("%s: got %q\nwant %q", key, details[key], want)
		}
	}
}
