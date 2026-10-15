package rules

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/graph"
)

func TestHexagonalCheck(t *testing.T) {
	architecture := Architecture{Style: StyleHexagonal, Layers: []Layer{{Name: graph.LayerDomain}, {Name: graph.LayerApplication}, {Name: graph.LayerAdapter}}}
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{graph.LayerDomain, graph.LayerDomain, true},
		{graph.LayerDomain, graph.LayerApplication, false},
		{graph.LayerApplication, graph.LayerDomain, true},
		{graph.LayerApplication, graph.LayerAdapter, false},
		{graph.LayerAdapter, graph.LayerApplication, true},
		{graph.LayerAdapter, graph.LayerAdapter, false},
		{graph.LayerUnclassified, graph.LayerAdapter, true},
		{graph.LayerComposition, graph.LayerAdapter, true},
		{graph.LayerAdapter, graph.LayerComposition, false},
		{graph.LayerDomain, graph.LayerComposition, false},
	} {
		got, rule := architecture.Check(Placement{Layer: c.from}, Placement{Layer: c.to})
		if got != c.want || got != (rule == "") {
			t.Errorf("Check(%s, %s) = %t, %q", c.from, c.to, got, rule)
		}
	}

	if got := architecture.MayUse(Placement{Layer: graph.LayerAdapter}); !slices.Equal(got, []string{graph.LayerDomain, graph.LayerApplication}) {
		t.Errorf("adapter may use %v", got)
	}
}

func TestLayeredIsRelaxed(t *testing.T) {
	architecture := layered()
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"presentation", "data", true},
		{"presentation", "business", true},
		{"business", "business", true},
		{"data", "business", false},
		{"business", "presentation", false},
	} {
		if got, _ := architecture.Check(Placement{Layer: c.from}, Placement{Layer: c.to}); got != c.want {
			t.Errorf("Check(%s, %s) = %t", c.from, c.to, got)
		}
	}

	if got := architecture.MayUse(Placement{Layer: "business"}); !slices.Equal(got, []string{"data", "business"}) {
		t.Errorf("business may use %v", got)
	}

	if got := architecture.Resolve("web/admin"); got.Layer != "presentation" {
		t.Errorf("web/admin resolved to %+v", got)
	}
}

func TestCleanPointsInward(t *testing.T) {
	architecture := Architecture{Style: StyleClean}
	for _, name := range CleanLayers {
		architecture.Layers = append(architecture.Layers, Layer{Name: name})
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	if ok, _ := architecture.Check(Placement{Layer: "frameworks"}, Placement{Layer: "entities"}); !ok {
		t.Error("frameworks should reach entities")
	}

	if ok, rule := architecture.Check(Placement{Layer: "use_cases"}, Placement{Layer: "interface_adapters"}); ok || !strings.Contains(rule, "inward") {
		t.Errorf("use_cases → interface_adapters: %t %q", ok, rule)
	}

	architecture.Layers = append(architecture.Layers, Layer{Name: "services"})
	if err := architecture.Validate(); !errors.Is(err, ErrArchitecture) {
		t.Errorf("an unknown clean layer should fail: %v", err)
	}
}

func TestBoundariesAreIndependent(t *testing.T) {
	architecture := Architecture{
		Style: StyleVertical, Composition: []string{"cmd/**"}, Shared: []string{"internal/platform/**"},
		Boundaries: []Boundary{{Name: "orders", Globs: []string{"internal/features/orders/**"}}, {Name: "billing", Globs: []string{"internal/features/billing/**"}}},
		Layers:     []Layer{{Name: "store", Globs: []string{"**/store/**"}}, {Name: "api", Globs: []string{"internal/features/*"}}},
	}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	g := graph.New()
	for _, path := range []string{"cmd/app", "internal/platform/log", "internal/features/orders", "internal/features/orders/store", "internal/features/billing", "internal/features/billing/store", "internal/features/billing/extra"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	uses("cmd/app", "internal/features/orders")
	uses("internal/features/orders", "internal/features/orders/store")
	uses("internal/features/orders", "internal/platform/log")
	uses("internal/features/billing/store", "internal/features/billing")
	uses("internal/features/billing", "internal/features/orders")
	uses("internal/platform/log", "internal/features/billing")
	g.Normalize()
	architecture.Assign(g)

	if m := g.Module("go:internal/features/orders/store"); m.Layer != "store" || m.Boundary != "orders" {
		t.Fatalf("orders/store placed at %s in %s", m.Layer, m.Boundary)
	}

	if m := g.Module("go:internal/features/billing/extra"); m.Layer != graph.LayerUnclassified || m.Boundary != "billing" {
		t.Fatalf("billing/extra placed at %s in %s", m.Layer, m.Boundary)
	}

	var got []string
	for _, violation := range architecture.Violations(g) {
		got = append(got, violation.From+" → "+violation.To)
	}

	want := []string{
		"go:internal/features/billing → go:internal/features/orders",
		"go:internal/features/billing/store → go:internal/features/billing",
		"go:internal/platform/log → go:internal/features/billing",
	}

	if !slices.Equal(got, want) {
		t.Fatalf("violations:\n%v\nwant\n%v", got, want)
	}
}

func TestModularPublicSurfaceAndCycles(t *testing.T) {
	architecture := Architecture{Style: StyleModular, Boundaries: []Boundary{
		{Name: "orders", Globs: []string{"internal/orders/**"}},
		{Name: "billing", Globs: []string{"internal/billing/**"}},
		{Name: "shipping", Globs: []string{"internal/shipping/**"}},
	}, Public: []string{"internal/*", "internal/*/api/**"}}
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	g := graph.New()
	for _, path := range []string{"internal/orders", "internal/orders/db", "internal/billing", "internal/billing/api", "internal/billing/ledger", "internal/shipping"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true})
	}

	uses := func(from, to string) {
		g.AddEdge(graph.Edge{From: "go:" + from + ":X", To: "go:" + to + ":X", Kind: graph.EdgeTypeUse})
	}

	uses("internal/orders", "internal/billing/api")
	uses("internal/orders", "internal/billing/ledger")
	uses("internal/billing", "internal/shipping")
	uses("internal/shipping", "internal/orders")
	uses("internal/orders/db", "internal/orders")
	g.Normalize()
	architecture.Assign(g)

	if m := g.Module("go:internal/billing/api"); !m.Public || m.Boundary != "billing" {
		t.Fatalf("billing/api: %+v", m)
	}

	kinds := map[string]string{}
	for _, violation := range architecture.Violations(g) {
		kinds[violation.From+" → "+violation.To] = violation.Kind
	}

	want := map[string]string{
		"go:internal/orders → go:internal/billing/ledger": FindingLayerViolation,
		"go:internal/orders → go:internal/billing/api":    FindingCycle,
		"go:internal/billing → go:internal/shipping":      FindingCycle,
		"go:internal/shipping → go:internal/orders":       FindingCycle,
	}

	if len(kinds) != len(want) {
		t.Fatalf("violations: %v", kinds)
	}

	for key, kind := range want {
		if kinds[key] != kind {
			t.Errorf("%s: got %q, want %q (all: %v)", key, kinds[key], kind, kinds)
		}
	}
}

func TestBoundaryPrecedence(t *testing.T) {
	architecture := Architecture{Style: StyleModular, Shared: []string{"lib/**"}, Boundaries: []Boundary{
		{Name: "orders", Globs: []string{"**/orders/**"}},
		{Name: "billing", Globs: []string{"web/billing/**", "store/billing/**", "web/billing/orders/legacy"}},
		{Name: "shipping", Globs: []string{"web/*/shipping/**"}},
		{Name: "legacy", Globs: []string{"legacy/**"}},
	}}

	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		path string
		want Placement
	}{
		{"web/orders", Placement{Layer: graph.LayerBoundary, Boundary: "orders", Public: true}},
		{"store/orders/db", Placement{Layer: graph.LayerBoundary, Boundary: "orders"}},
		{"web/billing", Placement{Layer: graph.LayerBoundary, Boundary: "billing", Public: true}},
		{"store/billing", Placement{Layer: graph.LayerBoundary, Boundary: "billing", Public: true}},
		// web/billing/** has two literal segments and **/orders/** one.
		{"web/billing/orders", Placement{Layer: graph.LayerBoundary, Boundary: "billing"}},
		// An exact path wins over every wildcard.
		{"web/billing/orders/legacy", Placement{Layer: graph.LayerBoundary, Boundary: "billing", Public: true}},
		// Shared comes before boundaries.
		{"lib/orders", Placement{Layer: graph.LayerShared}},
		// One literal segment each: a tie.
		{"legacy/orders", Placement{Layer: graph.LayerUnclassified, Ambiguous: []string{"orders", "legacy"}}},
		{"web/x/shipping/orders", Placement{Layer: graph.LayerBoundary, Boundary: "shipping"}},
		{"docs", Placement{Layer: graph.LayerUnclassified}},
	} {
		if got := architecture.Resolve(c.path); !got.Same(c.want) {
			t.Errorf("%s: got %+v, want %+v", c.path, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name         string
		architecture Architecture
	}{
		{"unknown style", Architecture{Style: "onion"}},
		{"layered without layers", Architecture{Style: StyleLayered}},
		{"vertical without boundaries", Architecture{Style: StyleVertical}},
		{"boundary without globs", Architecture{Style: StyleVertical, Boundaries: []Boundary{{Name: "orders"}}}},
		{"boundary without a name", Architecture{Style: StyleVertical, Boundaries: []Boundary{{Globs: []string{"x/**"}}}}},
		{"duplicate boundary", Architecture{Style: StyleModular, Boundaries: []Boundary{{Name: "a", Globs: []string{"a/**"}}, {Name: "a", Globs: []string{"b/**"}}}}},
		{"modular with layers", Architecture{Style: StyleModular, Boundaries: []Boundary{{Name: "a", Globs: []string{"a/**"}}}, Layers: []Layer{{Name: "api"}}}},
		{"shared outside boundaries", Architecture{Style: StyleLayered, Layers: []Layer{{Name: "web"}}, Shared: []string{"lib/**"}}},
		{"boundaries in layered", Architecture{Style: StyleLayered, Layers: []Layer{{Name: "web"}}, Boundaries: []Boundary{{Name: "a", Globs: []string{"a/**"}}}}},
		{"boundary as a layer name", Architecture{Style: StyleLayered, Layers: []Layer{{Name: "boundary"}}}},
		{"reserved layer name", Architecture{Style: StyleLayered, Layers: []Layer{{Name: "shared"}}}},
		{"duplicate layer", Architecture{Style: StyleLayered, Layers: []Layer{{Name: "web"}, {Name: "web"}}}},
	} {
		if err := c.architecture.Validate(); !errors.Is(err, ErrArchitecture) {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

func layered() Architecture {
	return Architecture{Style: StyleLayered, Layers: []Layer{
		{Name: "data", Globs: []string{"store/**"}},
		{Name: "business", Globs: []string{"service/**"}},
		{Name: "presentation", Globs: []string{"web/**"}},
	}}
}

func TestNoneAllowsEverything(t *testing.T) {
	architecture := Architecture{Style: StyleNone}
	if err := architecture.Validate(); err != nil {
		t.Fatal(err)
	}

	if Styles[0] != StyleNone {
		t.Fatalf("none is the default, listed first: %v", Styles)
	}

	g := graph.New()
	for _, path := range []string{"cmd/app", "web", "store"} {
		g.AddModule(&graph.Module{ID: "go:" + path, Language: "go", Path: path})
		g.AddSymbol(&graph.Symbol{ID: "go:" + path + ":X", Module: "go:" + path, Name: "X", Kind: graph.KindType, Contract: true, Signature: "type X struct"})
	}

	g.AddEdge(graph.Edge{From: "go:store:X", To: "go:cmd/app:X", Kind: graph.EdgeTypeUse})
	g.AddEdge(graph.Edge{From: "go:web:X", To: "go:store:X", Kind: graph.EdgeTypeUse})
	g.Normalize()
	architecture.Assign(g)
	for _, module := range g.Modules {
		if module.Layer != graph.LayerNone {
			t.Fatalf("every module is placed in none: %+v", module)
		}
	}

	if violations := architecture.Violations(g); len(violations) != 0 {
		t.Fatalf("none allows every dependency: %+v", violations)
	}

	findings := Rank(g, nil, nil, nil)
	for _, finding := range findings {
		if finding.Kind == FindingUnclassified {
			t.Fatalf("nothing is unclassified under none: %+v", finding)
		}
	}
}

func TestExactPathWinsOverWildcards(t *testing.T) {
	architecture := Architecture{
		Style:       StyleHexagonal,
		Composition: []string{"cmd/**"},
		Layers: []Layer{
			{Name: graph.LayerDomain, Globs: []string{"internal/**"}},
			{Name: graph.LayerAdapter, Side: graph.SideDriving, Globs: []string{"internal/web", "cmd/tool"}},
		},
	}

	for path, want := range map[string]Placement{
		"internal/core": {Layer: graph.LayerDomain},
		"internal/web":  {Layer: graph.LayerAdapter, Side: graph.SideDriving},
		"cmd/tool":      {Layer: graph.LayerAdapter, Side: graph.SideDriving},
		"cmd/server":    {Layer: graph.LayerComposition},
	} {
		if got := architecture.Resolve(path); !got.Same(want) {
			t.Errorf("Resolve(%s) = %+v, want %+v", path, got, want)
		}
	}
}

func TestReasons(t *testing.T) {
	modular := Architecture{Style: StyleModular, Shared: []string{"lib/**"}, Boundaries: []Boundary{
		{Name: "orders", Globs: []string{"**/orders/**"}},
		{Name: "billing", Globs: []string{"web/billing/**", "web/billing/orders/legacy"}},
		{Name: "legacy", Globs: []string{"legacy/**"}},
	}}

	hexagonal := Architecture{Style: StyleHexagonal, Composition: []string{"cmd/**"}, Layers: []Layer{
		{Name: graph.LayerDomain, Globs: []string{"core/**"}},
		{Name: graph.LayerAdapter, Side: graph.SideDriving, Globs: []string{"web/**", "core/web"}},
	}}

	vertical := Architecture{Style: StyleVertical, Boundaries: []Boundary{{Name: "orders", Globs: []string{"orders/**"}}}, Layers: []Layer{
		{Name: "api", Globs: []string{"**/api"}},
	}}

	for _, c := range []struct {
		architecture Architecture
		path         string
		want         []Reason
	}{
		{modular, "web/billing/orders", []Reason{{[]string{"boundaries", "billing"}, "web/billing/**"}}},
		{modular, "web/billing/orders/legacy", []Reason{{[]string{"boundaries", "billing"}, "web/billing/orders/legacy"}}},
		{modular, "lib/x", []Reason{{[]string{"shared"}, "lib/**"}}},
		{modular, "legacy/orders", []Reason{{[]string{"boundaries", "orders"}, "**/orders/**"}, {[]string{"boundaries", "legacy"}, "legacy/**"}}},
		{modular, "docs", nil},
		{hexagonal, "cmd/tool", []Reason{{[]string{"composition"}, "cmd/**"}}},
		{hexagonal, "core/web", []Reason{{[]string{"layers", "adapter", "driving"}, "core/web"}}},
		{hexagonal, "core/model", []Reason{{[]string{"layers", "domain"}, "core/**"}}},
		{vertical, "orders/api", []Reason{{[]string{"boundaries", "orders"}, "orders/**"}, {[]string{"layers", "api"}, "**/api"}}},
		{Architecture{Style: StyleNone}, "x", nil},
	} {
		got := c.architecture.Reasons(c.path)
		if !slices.EqualFunc(got, c.want, func(a, b Reason) bool { return a.Glob == b.Glob && slices.Equal(a.Path, b.Path) }) {
			t.Errorf("%s: got %+v, want %+v", c.path, got, c.want)
		}
	}
}
