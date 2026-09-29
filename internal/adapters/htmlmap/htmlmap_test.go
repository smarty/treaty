package htmlmap

import (
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

func TestLayoutHasNoOverlaps(t *testing.T) {
	layers := []struct{ layer, side string }{
		{graph.LayerDomain, ""}, {graph.LayerApplication, ""},
		{graph.LayerAdapter, graph.SideDriving}, {graph.LayerAdapter, graph.SideDriven},
		{graph.LayerUnclassified, ""}, {graph.LayerComposition, ""},
	}

	var view app.MapView
	for i := range 50 {
		kind := layers[i%len(layers)]
		id := fmt.Sprintf("go:m%02d", i)
		view.Modules = append(view.Modules, app.MapModule{ID: id, Path: id, Layer: kind.layer, Side: kind.side})
		for j := range 40 {
			view.Symbols = append(view.Symbols, app.MapSymbol{ID: fmt.Sprintf("%s:S%d", id, j), Module: id, Name: fmt.Sprintf("S%d", j), Kind: graph.KindFunction, Contract: true})
		}
	}

	started := time.Now()
	html, err := New().Render(view)
	if err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("render took %s", elapsed)
	}

	if strings.Contains(string(html), "/*DATA*/") {
		t.Fatal("data was not embedded")
	}

	layout := ComputeLayout(view)
	for _, module := range view.Modules {
		p, placed := layout.Modules[module.ID]
		if !placed {
			t.Errorf("%s (%s) was not placed", module.ID, module.Layer)
		}

		if module.Layer == graph.LayerComposition && p.X >= 0 {
			t.Errorf("composition %s is not on the left: %+v", module.ID, p)
		}
	}

	for i, a := range view.Modules {
		for _, b := range view.Modules[i+1:] {
			pa, okA := layout.Modules[a.ID]
			pb, okB := layout.Modules[b.ID]
			if !okA || !okB {
				continue
			}

			if distance := math.Hypot(pa.X-pb.X, pa.Y-pb.Y); distance < 2.8*moduleSize {
				t.Errorf("%s and %s overlap: %.1f apart", a.ID, b.ID, distance)
			}
		}
	}
}

func TestRenderEmptyView(t *testing.T) {
	html, err := New().Render(app.MapView{Modules: []app.MapModule{{ID: "go:a", Path: "a", Layer: graph.LayerDomain}}})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(html), `D[key] = D[key] || []`) {
		t.Fatal("the page must default missing lists, or an empty queue blanks the map")
	}
}

func TestNestedGroups(t *testing.T) {
	var modules []app.MapModule
	add := func(path, layer, side string) {
		modules = append(modules, app.MapModule{ID: "go:" + path, Path: path, Layer: layer, Side: side})
	}

	for _, path := range []string{"domain/billing", "domain/billing/invoice", "domain/billing/payment", "domain/shipping/rates", "domain/shipping/labels", "domain/catalog/core", "domain/catalog/pricing"} {
		add(path, graph.LayerDomain, "")
	}

	add("app", graph.LayerApplication, "")
	add("internal/adapters/db", graph.LayerAdapter, graph.SideDriven)
	add("internal/adapters/queue", graph.LayerAdapter, graph.SideDriven)
	add("internal/web", graph.LayerAdapter, graph.SideDriving)
	add("cmd/tool", graph.LayerComposition, "")

	layout := ComputeLayout(app.MapView{Modules: modules})
	groups := map[string]Group{}
	for _, group := range layout.Groups {
		groups[group.ID] = group
	}

	want := map[string]string{
		"domain:domain/billing":  "",
		"domain:domain/catalog":  "",
		"domain:domain/shipping": "",
	}

	if len(groups) != len(want) {
		t.Fatalf("groups: %v", layout.Groups)
	}

	for id, parent := range want {
		if group, ok := groups[id]; !ok || group.Parent != parent {
			t.Errorf("group %s: %+v", id, group)
		}
	}

	if p, placed := layout.Modules["go:cmd/tool"]; !placed || p.X >= 0 || math.Hypot(p.X, p.Y) < layout.Rings[1].Radius {
		t.Errorf("composition must sit on the left of the adapter ring: %+v", p)
	}

	inscribed := math.Cos(math.Pi / 6)
	for _, group := range layout.Groups {
		for _, id := range group.Modules {
			p := layout.Modules[id]
			if math.Hypot(p.X-group.X, p.Y-group.Y)+moduleSize > group.Radius*inscribed+0.5 {
				t.Errorf("%s sticks out of %s", id, group.ID)
			}
		}

		if parent, ok := groups[group.Parent]; ok && math.Hypot(group.X-parent.X, group.Y-parent.Y)+group.Radius > parent.Radius*inscribed+0.5 {
			t.Errorf("%s sticks out of %s", group.ID, parent.ID)
		}

		for _, other := range layout.Groups {
			if other.ID < group.ID && other.Parent == group.Parent && math.Hypot(group.X-other.X, group.Y-other.Y) < group.Radius+other.Radius {
				t.Errorf("%s and %s overlap", group.ID, other.ID)
			}
		}
	}

	for a, pa := range layout.Modules {
		for b, pb := range layout.Modules {
			if a < b && math.Hypot(pa.X-pb.X, pa.Y-pb.Y) < 2*moduleSize {
				t.Errorf("%s and %s overlap", a, b)
			}
		}
	}

	for _, group := range layout.Groups {
		if group.Layer == graph.LayerDomain && group.Parent == "" && math.Hypot(group.X, group.Y)+group.Radius > layout.Rings[0].Radius+0.5 {
			t.Errorf("%s leaves the domain ring", group.ID)
		}
	}
}

func TestRegionLayouts(t *testing.T) {
	type spec struct{ path, layer, section string }
	for _, c := range []struct {
		name  string
		view  app.MapView
		specs []spec
	}{
		{"layered", app.MapView{Architecture: rules.StyleLayered, Layers: []string{"data", "business", "presentation"}}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"web/admin", "presentation", ""}, {"web/public", "presentation", ""},
			{"service/orders", "business", ""}, {"service/billing", "business", ""}, {"store/sql", "data", ""}, {"lonely", graph.LayerUnclassified, ""},
		}},
		{"slices", app.MapView{Architecture: rules.StyleSlices, Layers: []string{"store", "api"}}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"platform/log", graph.LayerShared, ""},
			{"features/orders", "api", "features/orders"}, {"features/orders/store", "store", "features/orders"},
			{"features/billing", "api", "features/billing"}, {"features/billing/store", "store", "features/billing"}, {"features/billing/extra", graph.LayerUnclassified, "features/billing"},
		}},
		{"modular", app.MapView{Architecture: rules.StyleModular}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"platform/log", graph.LayerShared, ""},
			{"internal/orders", graph.LayerContext, "internal/orders"}, {"internal/orders/db", graph.LayerContext, "internal/orders"},
			{"internal/billing", graph.LayerContext, "internal/billing"}, {"internal/shipping", graph.LayerContext, "internal/shipping"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, each := range c.specs {
				c.view.Modules = append(c.view.Modules, app.MapModule{ID: "go:" + each.path, Path: each.path, Layer: each.layer, Section: each.section})
			}

			layout := ComputeLayout(c.view)
			if len(layout.Rings) != 0 || len(layout.Regions) == 0 {
				t.Fatalf("expected regions and no rings: %+v", layout)
			}

			for _, module := range c.view.Modules {
				p, placed := layout.Modules[module.ID]
				if !placed {
					t.Fatalf("%s was not placed", module.ID)
				}

				if math.Abs(p.X) > layout.Extent || math.Abs(p.Y) > layout.Extent {
					t.Errorf("%s at %+v is outside the extent %.1f", module.ID, p, layout.Extent)
				}

				want := layerLabel(module.Layer)
				if module.Section != "" && c.view.Architecture == rules.StyleModular {
					want = path.Base(module.Section)
				}

				inside := false
				for _, region := range layout.Regions {
					if region.Label == want && p.X-moduleSize >= region.X && p.X+moduleSize <= region.X+region.W && p.Y-moduleSize >= region.Y && p.Y+moduleSize <= region.Y+region.H {
						inside = true
					}
				}

				if !inside {
					t.Errorf("%s at %+v is not inside a %q region: %+v", module.ID, p, want, layout.Regions)
				}
			}

			for a, pa := range layout.Modules {
				for b, pb := range layout.Modules {
					if a < b && math.Hypot(pa.X-pb.X, pa.Y-pb.Y) < 2*moduleSize {
						t.Errorf("%s and %s overlap", a, b)
					}
				}
			}
		})
	}
}

func TestCleanRings(t *testing.T) {
	view := app.MapView{Architecture: rules.StyleClean, Layers: rules.CleanLayers}
	for _, layer := range append(slices.Clone(rules.CleanLayers), graph.LayerComposition, "frameworks", "frameworks") {
		id := fmt.Sprintf("go:m%d", len(view.Modules))
		view.Modules = append(view.Modules, app.MapModule{ID: id, Path: id, Layer: layer})
	}

	layout := ComputeLayout(view)
	if len(layout.Rings) != 4 || layout.Rings[0].Layer != "entities" || layout.Rings[3].Layer != "frameworks" || len(layout.Labels) != 0 {
		t.Fatalf("rings: %+v, labels %+v", layout.Rings, layout.Labels)
	}

	for i, module := range view.Modules {
		p := layout.Modules[module.ID]
		ring := slices.Index(rules.CleanLayers, module.Layer)
		if ring < 0 {
			ring = 3
		}

		inner := 0.0
		if ring > 0 {
			inner = layout.Rings[ring-1].Radius * math.Cos(math.Pi/6)
		}

		if distance := math.Hypot(p.X, p.Y); distance < inner || distance > layout.Rings[ring].Radius {
			t.Errorf("module %d (%s) at %.1f is outside its ring (%.1f to %.1f)", i, module.Layer, distance, inner, layout.Rings[ring].Radius)
		}
	}
}
