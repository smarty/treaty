package rules

import (
	"slices"
	"testing"

	"github.com/smarty/treaty/internal/graph"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		glob, value string
		want        bool
	}{
		{"internal/graph/**", "internal/graph", true},
		{"internal/graph/**", "internal/graph/sub/deep", true},
		{"internal/graph/**", "internal/graphs", false},
		{"injection", "injection", true},
		{"injection", "injection/scope", false},
		{"adapters/*x", "adapters/reflectx", true},
		{"go:adapters/**", "go:adapters/otelx", true},
	} {
		if got := Match(c.glob, c.value); got != c.want {
			t.Errorf("Match(%q, %q) = %t", c.glob, c.value, got)
		}
	}
}

func TestClassifyAndRank(t *testing.T) {
	base := sample(func(g *graph.Graph) {})
	head := sample(func(g *graph.Graph) {
		g.Symbol("go:app:Run").Signature = "func Run(ctx context.Context) error"
		g.Symbol("go:app:helper").Hash = "changed"
		g.Symbol("go:app:Config").Fields = append(g.Symbol("go:app:Config").Fields, graph.Field{Text: "Extra int", Contract: true})
		g.AddSymbol(&graph.Symbol{ID: "go:app:Port.Close", Module: "go:app", Name: "Port.Close", Parent: "Port", Kind: graph.KindMethod, Contract: true, Signature: "func Close() error"})
	})

	changes := map[string]string{}
	for _, change := range Classify(base, head, DefaultCompatible) {
		changes[change.Symbol] = change.Kind
	}

	want := map[string]string{
		"go:app:Run":        ChangeBreaking,
		"go:app:helper":     ChangeImplementation,
		"go:app:Config":     ChangeContract,
		"go:app:Port":       ChangeBreaking,
		"go:app:Port.Close": ChangeAdded,
		"go:app:Gone":       "",
	}

	for id, kind := range want {
		if changes[id] != kind {
			t.Errorf("%s: got %q, want %q", id, changes[id], kind)
		}
	}

	findings := Rank(head, base, Classify(base, head, DefaultCompatible), nil)
	if findings[0].Severity != SeverityHigh || findings[len(findings)-1].Kind != FindingImplementation {
		t.Fatalf("queue order: %+v", findings)
	}
}

func TestViolationsAndMetrics(t *testing.T) {
	g := sample(func(g *graph.Graph) {})
	architecture := Architecture{Style: StyleHexagonal, Layers: []Layer{{Name: graph.LayerDomain, Globs: []string{"core"}}, {Name: graph.LayerApplication, Globs: []string{"app"}}}}
	architecture.Assign(g)
	violations := architecture.Violations(g)
	if len(violations) != 1 || violations[0].From != "go:core" || violations[0].To != "go:app" {
		t.Fatalf("violations: %+v", violations)
	}

	metrics := Metrics(g)
	if metrics["go:core"].Instability != 0.5 || metrics["go:app"].Abstractness != 0.33 {
		t.Fatalf("metrics: %+v", metrics)
	}
}

func sample(edit func(g *graph.Graph)) *graph.Graph {
	g := graph.New()
	g.AddModule(&graph.Module{ID: "go:app", Language: "go", Path: "app"})
	g.AddModule(&graph.Module{ID: "go:core", Language: "go", Path: "core"})
	for _, s := range []*graph.Symbol{
		{ID: "go:app:Run", Name: "Run", Kind: graph.KindFunction, Signature: "func Run() error"},
		{ID: "go:app:helper", Name: "helper", Kind: graph.KindFunction, Signature: "func helper()", Hash: "a"},
		{ID: "go:app:Config", Name: "Config", Kind: graph.KindType, Signature: "type Config struct", Fields: []graph.Field{{Text: "Name string", Contract: true}}},
		{ID: "go:app:Port", Name: "Port", Kind: graph.KindInterface, Signature: "type Port interface"},
		{ID: "go:core:Thing", Name: "Thing", Kind: graph.KindType, Signature: "type Thing int"},
	} {
		s.Module, _ = graph.SplitSymbolID(s.ID)
		s.Contract = s.Name != "helper"
		g.AddSymbol(s)
	}

	g.AddEdge(graph.Edge{From: "go:core:Thing", To: "go:app:Config", Kind: graph.EdgeTypeUse})
	g.AddEdge(graph.Edge{From: "go:app:Run", To: "go:core:Thing", Kind: graph.EdgeTypeUse})
	edit(g)
	g.Normalize()
	return g
}

func TestMovesAndFieldDetail(t *testing.T) {
	base := sample(func(g *graph.Graph) {
		g.AddSymbol(&graph.Symbol{ID: "go:app:Old", Module: "go:app", Name: "Old", Kind: graph.KindType, Contract: true, Signature: "type Old struct"})
		g.AddSymbol(&graph.Symbol{ID: "go:app:Old.Go", Module: "go:app", Name: "Old.Go", Parent: "Old", Kind: graph.KindMethod, Contract: true, Signature: "func Go()"})
	})
	head := sample(func(g *graph.Graph) {
		g.Symbol("go:app:Config").Fields = []graph.Field{{Text: "Title string", Contract: true}}
		moved := *g.Symbol("go:app:Port")
		moved.ID, moved.Module = "go:core:Port", "go:core"
		g.AddSymbol(&moved)
		g.AddSymbol(&graph.Symbol{ID: "go:app:New", Module: "go:app", Name: "New", Kind: graph.KindType, Contract: true, Signature: "type New struct"})
		g.AddSymbol(&graph.Symbol{ID: "go:app:New.Go", Module: "go:app", Name: "New.Go", Parent: "New", Kind: graph.KindMethod, Contract: true, Signature: "func Go()"})
	})
	head.Symbols = slices.DeleteFunc(head.Symbols, func(s *graph.Symbol) bool { return s.ID == "go:app:Port" })
	head = rebuild(head)

	byID := map[string]Change{}
	for _, change := range Classify(base, head, DefaultCompatible) {
		byID[change.Symbol] = change
	}

	if change := byID["go:core:Port"]; change.Kind != ChangeMoved || change.From != "go:app:Port" {
		t.Errorf("move: %+v", change)
	}

	if change := byID["go:app:New"]; change.Kind != ChangeMoved || change.From != "go:app:Old" {
		t.Errorf("rename: %+v", change)
	}

	for _, id := range []string{"go:app:Port", "go:app:Old", "go:app:Old.Go", "go:app:New.Go"} {
		if change, ok := byID[id]; ok {
			t.Errorf("%s should fold into its move: %+v", id, change)
		}
	}

	if change := byID["go:app:Config"]; change.Kind != ChangeBreaking || !slices.Equal(change.FieldsRemoved, []string{"Name string"}) || !slices.Equal(change.FieldsAdded, []string{"Title string"}) {
		t.Errorf("fields: %+v", change)
	}
}

func TestVariantMismatch(t *testing.T) {
	head := sample(func(g *graph.Graph) {
		g.Symbol("go:app:Run").Variants = []graph.Variant{{File: "app/run_arm64.go", Line: 3, Signature: "func Run() (err error)"}}
		g.Symbol("go:app:Config").Variants = []graph.Variant{{File: "app/config_arm64.go", Line: 3, Signature: "type Config struct", Fields: []graph.Field{{Text: "Name string", Contract: true}}}}
	})

	var titles []string
	for _, finding := range Rank(head, nil, nil, nil) {
		if finding.Kind == FindingVariants {
			titles = append(titles, finding.Title)
		}
	}

	if !slices.Equal(titles, []string{"Build variants of go:app:Run disagree"}) {
		t.Fatalf("variant findings: %v", titles)
	}
}

func rebuild(g *graph.Graph) *graph.Graph {
	result := graph.New()
	for _, module := range g.Modules {
		result.AddModule(module)
	}

	for _, symbol := range g.Symbols {
		result.AddSymbol(symbol)
	}

	for _, edge := range g.Edges {
		result.AddEdge(edge)
	}

	result.Normalize()
	return result
}

func TestClassifyReach(t *testing.T) {
	build := func(signature string, callerHash string, withEntry bool) *graph.Graph {
		g := graph.New()
		g.AddModule(&graph.Module{ID: "go:internal/store", Language: "go", Path: "internal/store", Private: true})
		g.AddModule(&graph.Module{ID: "go:internal/web", Language: "go", Path: "internal/web", Private: true})
		g.AddModule(&graph.Module{ID: "go:pkg", Language: "go", Path: "pkg"})
		g.AddSymbol(&graph.Symbol{ID: "go:internal/store:Load", Module: "go:internal/store", Name: "Load", Kind: graph.KindFunction, Contract: true, Signature: signature})
		g.AddSymbol(&graph.Symbol{ID: "go:internal/web:Get", Module: "go:internal/web", Name: "Get", Kind: graph.KindFunction, Contract: true, Signature: "func Get()", Hash: callerHash})
		g.AddSymbol(&graph.Symbol{ID: "go:pkg:Open", Module: "go:pkg", Name: "Open", Kind: graph.KindFunction, Contract: true, Signature: signature})
		g.AddEdge(graph.Edge{From: "go:internal/web:Get", To: "go:internal/store:Load", Kind: graph.EdgeCall})
		if withEntry {
			g.AddModule(&graph.Module{ID: "go:.", Language: "go", Path: ".", Entry: true})
			g.AddSymbol(&graph.Symbol{ID: "go:.:Run", Module: "go:.", Name: "Run", Kind: graph.KindFunction, Contract: true, Signature: signature})
			g.AddSymbol(&graph.Symbol{ID: "go:.:Gone", Module: "go:.", Name: "Gone", Kind: graph.KindFunction, Contract: true, Signature: "func Gone()"})
		}

		g.Normalize()
		return g
	}

	kinds := func(changes []Change) map[string]Change {
		result := map[string]Change{}
		for _, change := range changes {
			result[change.Symbol] = change
		}

		return result
	}

	base := build("func Load() int", "a", true)
	head := build("func Load() string", "b", true)
	head.Symbols = slices.DeleteFunc(head.Symbols, func(s *graph.Symbol) bool { return s.ID == "go:.:Gone" })
	head = rebuild(head)
	changes := kinds(Classify(base, head, DefaultCompatible))
	if load := changes["go:internal/store:Load"]; load.Kind != ChangeBreaking || !load.Absorbed {
		t.Fatalf("a private change whose callers all changed is absorbed but still breaking: %+v", load)
	}

	if open := changes["go:pkg:Open"]; open.Kind != ChangeBreaking || open.Absorbed {
		t.Fatalf("a public change is never absorbed: %+v", open)
	}

	if run := changes["go:.:Run"]; run.Kind != ChangeImplementation {
		t.Fatalf("nothing imports an entry module, so its breaking change is implementation: %+v", run)
	}

	if _, listed := changes["go:.:Gone"]; listed {
		t.Fatal("a removal from an entry module is not listed")
	}

	unchangedCaller := kinds(Classify(build("func Load() int", "a", false), build("func Load() string", "a", false), DefaultCompatible))
	if load := unchangedCaller["go:internal/store:Load"]; load.Kind != ChangeBreaking || load.Absorbed {
		t.Fatalf("a caller that did not change keeps the change unabsorbed: %+v", load)
	}

	findings := Rank(head, base, Classify(base, head, DefaultCompatible), nil)
	for _, finding := range findings {
		if finding.Targets[0] == "go:internal/store:Load" && (finding.Kind != FindingBreaking || finding.Severity != SeverityMedium) {
			t.Fatalf("an absorbed change is a medium breaking finding: %+v", finding)
		}
	}
}
