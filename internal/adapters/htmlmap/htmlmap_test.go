package htmlmap

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
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

	layout := ComputeLayout(view.Modules)
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

	layout := ComputeLayout(modules)
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
