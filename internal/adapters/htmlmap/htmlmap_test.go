package htmlmap

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
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
		// Modules hold one to eight files, so their hexagons differ in size.
		var files []string
		for f := range 1 + i%8 {
			files = append(files, fmt.Sprintf("%s/f%d.go", id, f))
		}

		view.Modules = append(view.Modules, app.MapModule{ID: id, Path: id, Layer: kind.layer, Side: kind.side, Files: files})
		for j := range 40 {
			view.Symbols = append(view.Symbols, app.MapSymbol{ID: fmt.Sprintf("%s:S%d", id, j), Module: id, Name: fmt.Sprintf("S%d", j), Kind: graph.KindFunction, Contract: true, File: files[j%len(files)]})
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
	rings := map[string]float64{}
	for _, ring := range layout.Rings {
		rings[ring.Layer] = ring.Radius
	}

	inscribed := math.Cos(math.Pi / 6)
	for _, module := range view.Modules {
		p, placed := layout.Modules[module.ID]
		if !placed {
			t.Errorf("%s (%s) was not placed", module.ID, module.Layer)
		}

		if distance := math.Hypot(p.X, p.Y); module.Layer == graph.LayerComposition && (p.X >= 0 || distance < rings[graph.LayerAdapter]*inscribed || distance > rings[graph.LayerComposition]) {
			t.Errorf("composition %s is not in its half ring, left of the adapters: %+v", module.ID, p)
		}
	}

	for i, a := range view.Modules {
		for _, b := range view.Modules[i+1:] {
			pa, okA := layout.Modules[a.ID]
			pb, okB := layout.Modules[b.ID]
			if !okA || !okB {
				continue
			}

			if distance := math.Hypot(pa.X-pb.X, pa.Y-pb.Y); distance < layout.Sizes[a.ID]+layout.Sizes[b.ID] {
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

func TestHexagonalDrawsAnEmptyCompositionRing(t *testing.T) {
	layout := ComputeLayout(app.MapView{Architecture: rules.StyleHexagonal, Modules: []app.MapModule{{ID: "go:core", Path: "core", Layer: graph.LayerDomain, Files: []string{"core/core.go"}}}})
	if len(layout.Rings) != 4 || layout.Rings[3].Layer != graph.LayerComposition || !layout.Rings[3].Left || layout.Rings[3].Radius <= layout.Rings[2].Radius {
		t.Fatalf("hexagonal draws a composition half ring left of the adapters even when it is empty, so a module can move there: %+v", layout.Rings)
	}

	clean := ComputeLayout(app.MapView{Architecture: rules.StyleClean, Modules: []app.MapModule{{ID: "go:core", Path: "core", Layer: graph.LayerDomain}}})
	for _, ring := range clean.Rings {
		if ring.Layer == graph.LayerComposition {
			t.Errorf("clean keeps composition on its outer ring: %+v", clean.Rings)
		}
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

	inscribed := math.Cos(math.Pi / 6)
	last := layout.Rings[len(layout.Rings)-1]
	if p, placed := layout.Modules["go:cmd/tool"]; !placed || last.Layer != graph.LayerComposition || !last.Left || p.X >= 0 || math.Hypot(p.X, p.Y) < layout.Rings[len(layout.Rings)-2].Radius*inscribed {
		t.Errorf("composition must sit in its own half ring left of the adapters: %+v, rings %+v", p, layout.Rings)
	}
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
		{"none", app.MapView{Architecture: rules.StyleNone}, []spec{
			{"cmd/app", graph.LayerNone, ""}, {"web", graph.LayerNone, ""}, {"store", graph.LayerNone, ""},
		}},
		{"layered", app.MapView{Architecture: rules.StyleLayered, Layers: []string{"data", "business", "presentation"}}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"web/admin", "presentation", ""}, {"web/public", "presentation", ""},
			{"service/orders", "business", ""}, {"service/billing", "business", ""}, {"store/sql", "data", ""}, {"lonely", graph.LayerUnclassified, ""},
		}},
		{"vertical", app.MapView{Architecture: rules.StyleVertical, Layers: []string{"store", "api"}}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"platform/log", graph.LayerShared, ""},
			{"features/orders", "api", "orders"}, {"features/orders/store", "store", "orders"},
			{"features/billing", "api", "billing"}, {"features/billing/store", "store", "billing"}, {"features/billing/extra", graph.LayerUnclassified, "billing"},
		}},
		{"modular", app.MapView{Architecture: rules.StyleModular}, []spec{
			{"cmd/app", graph.LayerComposition, ""}, {"platform/log", graph.LayerShared, ""},
			{"internal/orders", graph.LayerBoundary, "orders"}, {"internal/orders/db", graph.LayerBoundary, "orders"},
			{"internal/billing", graph.LayerBoundary, "billing"}, {"internal/shipping", graph.LayerBoundary, "shipping"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, each := range c.specs {
				c.view.Modules = append(c.view.Modules, app.MapModule{ID: "go:" + each.path, Path: each.path, Layer: each.layer, Boundary: each.section})
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
				if module.Boundary != "" && c.view.Architecture == rules.StyleModular {
					want = module.Boundary
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

func TestFileCells(t *testing.T) {
	view := app.MapView{Modules: []app.MapModule{
		{ID: "go:app", Path: "app", Layer: graph.LayerApplication, Files: []string{"app/a.go", "app/b.go", "app/c.go", "app/d.go", "app/e.go", "app/f.go", "app/g.go", "app/h.go", "app/i.go"}},
		{ID: "go:core", Path: "core", Layer: graph.LayerDomain, Files: []string{"core/core.go"}},
		{ID: "go:planned", Path: "planned", Layer: graph.LayerDomain, Design: true},
	}}

	for i := range 5 {
		view.Symbols = append(view.Symbols, app.MapSymbol{ID: fmt.Sprintf("go:app:S%d", i), Module: "go:app", File: "app/e.go"})
	}

	view.Symbols = append(view.Symbols, app.MapSymbol{ID: "go:app:T", Module: "go:app", File: "app/a.go"})
	layout := ComputeLayout(view)

	if layout.Sizes["go:planned"] != moduleSize || layout.Sizes["go:core"] != moduleSize || len(layout.Cells["go:planned"]) != 0 {
		t.Fatalf("sizes %v, cells %v", layout.Sizes, layout.Cells["go:planned"])
	}

	cells := layout.Cells["go:app"]
	if len(cells) != 9 || layout.Sizes["go:app"] <= moduleSize {
		t.Fatalf("app: %d cells, size %.1f", len(cells), layout.Sizes["go:app"])
	}

	if cells[0].File != "app/a.go" || cells[0].Symbols != 1 || cells[4].File != "app/e.go" || cells[4].Symbols != 5 {
		t.Fatalf("cells must run in file order and count symbols: %+v", cells)
	}

	// The files sit evenly around one ring, the first at the top, and none
	// in the middle.
	ring := math.Hypot(cells[0].X, cells[0].Y)
	for i, cell := range cells {
		want := -math.Pi/2 + 2*math.Pi*float64(i)/float64(len(cells))
		if math.Abs(cell.Angle-want) > 0.001 || math.Abs(math.Hypot(cell.X, cell.Y)-ring) > 0.2 || ring < cellSize {
			t.Errorf("%s at %.1f, %.1f, angle %.3f; want angle %.3f on a ring of %.1f", cell.File, cell.X, cell.Y, cell.Angle, want, ring)
		}
	}

	if only := layout.Cells["go:core"]; len(only) != 1 || only[0].X != 0 || only[0].Y != 0 {
		t.Errorf("a single file sits in the middle: %+v", only)
	}

	inscribed := math.Cos(math.Pi / 6)
	for i, a := range cells {
		if math.Hypot(a.X, a.Y)+cellSize > layout.Sizes["go:app"]*inscribed+0.5 {
			t.Errorf("%s sticks out of its module", a.File)
		}

		for _, b := range cells[i+1:] {
			if math.Hypot(a.X-b.X, a.Y-b.Y) < 2*cellSize*inscribed-0.5 {
				t.Errorf("%s and %s overlap", a.File, b.File)
			}
		}
	}

	// A larger module takes more room, so nothing overlaps it.
	for a, pa := range layout.Modules {
		for b, pb := range layout.Modules {
			if a < b && math.Hypot(pa.X-pb.X, pa.Y-pb.Y) < layout.Sizes[a]+layout.Sizes[b] {
				t.Errorf("%s and %s overlap", a, b)
			}
		}
	}
}

func TestManifestTakesTheFirstCell(t *testing.T) {
	view := app.MapView{Modules: []app.MapModule{
		{ID: "go:tools", Path: "tools", Layer: graph.LayerDomain, Files: []string{"tools/z.go", "tools/a.go"}, Manifest: "tools/go.mod"},
		{ID: "go:lone", Path: "lone", Layer: graph.LayerDomain, Files: []string{"lone/lone.go"}},
	}}

	layout := ComputeLayout(view)
	cells := layout.Cells["go:tools"]
	if len(cells) != 3 || !cells[0].Manifest || cells[0].File != "tools/go.mod" || cells[1].File != "tools/a.go" || cells[1].Manifest || cells[2].File != "tools/z.go" {
		t.Fatalf("the go.mod comes first, then the files in name order: %+v", cells)
	}

	if cells[0].X != 0 || cells[0].Y >= 0 {
		t.Fatalf("the go.mod sits at 12:00: %+v", cells[0])
	}

	if layout.Sizes["go:tools"] <= layout.Sizes["go:lone"] {
		t.Fatalf("the go.mod's cell takes room like a file's: %v", layout.Sizes)
	}
}

func TestHoneycombRings(t *testing.T) {
	slots := honeycomb(19, 10)
	for i, a := range slots {
		want := 10.0
		if i == 0 {
			want = 0
		} else if i > 6 {
			want = 17.3
		}

		if distance := math.Hypot(a[0], a[1]); distance < want-0.1 || distance > 20.1 {
			t.Errorf("slot %d at %.1f from the center", i, distance)
		}

		for j, b := range slots[i+1:] {
			if math.Hypot(a[0]-b[0], a[1]-b[1]) < 10-0.01 {
				t.Errorf("slots %d and %d overlap", i, i+1+j)
			}
		}
	}
}

func TestPageInlinesEveryScript(t *testing.T) {
	entries, err := assets.ReadDir("page/js")
	if err != nil {
		t.Fatal(err)
	}

	var files []string
	for _, entry := range entries {
		files = append(files, entry.Name())
	}

	listed := slices.Clone(scripts)
	slices.Sort(listed)
	if !slices.Equal(files, listed) {
		t.Errorf("page/js holds %q but the page joins %q", files, scripts)
	}

	if strings.Contains(page, "/*STYLE*/") || strings.Contains(page, "/*SCRIPT*/") || !strings.Contains(page, "/*DATA*/null") {
		t.Error("the page must inline its style and scripts and keep the data marker")
	}
}

// The page's script is plain JavaScript with no build step, so a syntax
// error would surface only in a browser. Node, when installed, catches it.
func TestPageScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	var script strings.Builder
	for _, name := range scripts {
		data, err := assets.ReadFile("page/js/" + name)
		if err != nil {
			t.Fatal(err)
		}

		script.Write(data)
		script.WriteString("\n")
	}

	file := filepath.Join(t.TempDir(), "page.js")
	if err := os.WriteFile(file, []byte(script.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if output, err := exec.Command(node, "--check", file).CombinedOutput(); err != nil {
		t.Fatalf("the page's script does not parse: %v\n%s", err, output)
	}
}

func TestGroupMetrics(t *testing.T) {
	var modules []app.MapModule
	for _, path := range []string{"domain/billing", "domain/billing/invoice", "domain/billing/payment", "domain/catalog/core", "domain/catalog/pricing"} {
		modules = append(modules, app.MapModule{ID: "go:" + path, Path: path, Layer: graph.LayerDomain})
	}

	modules = append(modules, app.MapModule{ID: "go:app", Path: "app", Layer: graph.LayerApplication}, app.MapModule{ID: "go:old", Path: "old", Layer: graph.LayerApplication, Removed: true})
	view := app.MapView{
		Modules: modules,
		Edges: []app.MapEdge{
			{From: "go:app", To: "go:domain/billing"},
			{From: "go:app", To: "go:domain/billing/invoice"},
			{From: "go:domain/billing", To: "go:domain/billing/invoice"},
			{From: "go:domain/billing/payment", To: "go:domain/catalog/core"},
			{From: "go:domain/catalog/pricing", To: "go:domain/billing", Removed: true},
			{From: "go:old", To: "go:domain/billing"},
		},
		Symbols: []app.MapSymbol{
			{ID: "go:domain/billing:Store", Module: "go:domain/billing", Kind: graph.KindInterface, Contract: true},
			{ID: "go:domain/billing:New", Module: "go:domain/billing", Kind: graph.KindFunction, Contract: true},
			{ID: "go:domain/billing/invoice:Invoice", Module: "go:domain/billing/invoice", Kind: graph.KindType, Contract: true},
			{ID: "go:domain/billing/invoice:Invoice.Total", Module: "go:domain/billing/invoice", Parent: "Invoice", Kind: graph.KindMethod, Contract: true},
			{ID: "go:domain/billing/payment:charge", Module: "go:domain/billing/payment", Kind: graph.KindFunction},
			{ID: "go:domain/billing:Gone", Module: "go:domain/billing", Kind: graph.KindInterface, Contract: true, Removed: true},
		},
	}

	var billing Group
	for _, group := range ComputeLayout(view).Groups {
		if group.Path == "domain/billing" {
			billing = group
		}
	}

	// One module outside uses the group and it uses one; edges inside it,
	// removed edges and removed modules do not count. Of its three
	// top-level contracts, one is an interface.
	expected := rules.Metric{Afferent: 1, Efferent: 1, Instability: 0.5, Abstractness: 0.33, Distance: 0.17}
	if billing.Metrics != expected || !slices.Equal(billing.Uses, []string{"go:domain/catalog/core"}) || !slices.Equal(billing.UsedBy, []string{"go:app"}) {
		t.Fatalf("billing: %+v", billing)
	}
}

// TestTermsFindPlacingLines runs the Terms tab's search for the line of a
// placing glob in node, on treaty.yaml written the ways people write it.
func TestTermsFindPlacingLines(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	config, err := assets.ReadFile("page/js/config.js")
	if err != nil {
		t.Fatal(err)
	}

	cases := `
const hexagonal = ["version: 1", "architecture: hexagonal", "layers:", "  composition: [\"cmd/**\"]", "  domain:      [\"internal/graph/**\", \"internal/rules/**\"]", "  adapter:", "    driving:   [\"web/**\"]", "    driven:    [\"store/**\", \"queue/**\"]"];
const vertical = ["architecture: vertical", "composition: ['cmd/**']", "boundaries:", "  orders:", "    - \"**/orders/**\"   # all of it", "  billing: ['web/billing/**', store/billing/**]", "layers:", "  api: [\"**/api\"]"];
const snowflake = ["architecture: snowflake", "composition: [\".\", \"cmd/**\", \"internal/factory/**\"]", "stars: # each star's directory", "  \".\": []", "  internal: [\"internal/core/**\"]", "  internal/blocks: [\"internal/blocks/core/**\"]   # a branch"];
const loose = ["architecture: snowflake", "stars:", "  internal/: [\"internal/core/**\"]", "  ./internal/gate: []", "  ./: []"];
const cases = [
  [hexagonal, ["composition"], "cmd/**", 4],
  [hexagonal, ["layers", "domain"], "internal/rules/**", 5],
  [hexagonal, ["layers", "adapter", "driven"], "queue/**", 8],
  [hexagonal, ["layers", "adapter", "driving"], "gone/**", 7],
  [vertical, ["composition"], "cmd/**", 2],
  [vertical, ["boundaries", "orders"], "**/orders/**", 5],
  [vertical, ["boundaries", "billing"], "store/billing/**", 6],
  [vertical, ["layers", "api"], "**/api", 8],
  [vertical, ["shared"], "lib/**", 0],
  [snowflake, ["composition"], "internal/factory/**", 2],
  [snowflake, ["stars", "internal"], "internal/core/**", 5],
  [snowflake, ["stars", "internal/blocks"], "internal/blocks/core/**", 6],
  [snowflake, ["stars", "internal/blocks"], "", 6],
  [snowflake, ["stars", "."], "", 4],
  [loose, ["stars", "internal"], "internal/core/**", 3],
  [loose, ["stars", "internal/gate"], "", 4],
  [loose, ["stars", "."], "", 5],
];
for (const [lines, path, glob, want] of cases) {
  const got = locateReason(lines, { path, glob });
  if (got !== want) { console.log(path.join(" > ") + " " + glob + ": line " + got + ", want " + want); process.exitCode = 1; }
}
`
	file := filepath.Join(t.TempDir(), "terms.js")
	if err := os.WriteFile(file, append(config, []byte(cases)...), 0o644); err != nil {
		t.Fatal(err)
	}

	if output, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

// snowflakeView places agentportal-guard's target layout, and any extra
// paths, by the rules' own snowflake resolution.
func snowflakeView(extra ...string) app.MapView {
	architecture := rules.Architecture{
		Style:       rules.StyleSnowflake,
		Composition: []string{".", "cmd/**", "internal/factory/**"},
		Stars: []rules.Star{
			{Dir: "internal", Core: []string{"internal/core/**"}},
			{Dir: "internal/blocks", Core: []string{"internal/blocks/core/**"}},
			{Dir: "internal/gate", Core: []string{"internal/gate/core/**"}},
		},
	}

	paths := append([]string{
		".", "cmd/guardprobe", "internal/factory",
		"internal", "internal/core", "internal/core/trust", "internal/core/plans",
		"internal/ai", "internal/anthropic", "internal/plan", "internal/tensors", "internal/tracer", "internal/web", "internal/web/fetch",
		"internal/blocks", "internal/blocks/core", "internal/blocks/cleanse", "internal/blocks/spotlight", "internal/blocks/summarize", "internal/blocks/www", "internal/blocks/www/sub",
		"internal/gate", "internal/gate/core", "internal/gate/presence", "internal/gate/tagger",
		"scripts/x",
	}, extra...)

	view := app.MapView{Architecture: rules.StyleSnowflake, Root: "agentportal-guard", Layers: []string{graph.LayerCore, graph.LayerSpoke}, Stars: architecture.Tree(paths)}
	for i, modulePath := range paths {
		place := architecture.Resolve(modulePath)
		var files []string
		for f := range 1 + i%5 {
			files = append(files, fmt.Sprintf("%s/f%d.go", modulePath, f))
		}

		view.Modules = append(view.Modules, app.MapModule{ID: "go:" + modulePath, Path: modulePath, Layer: place.Layer, Star: place.Star, Spoke: place.Spoke, Files: files})
	}

	return view
}

func TestSnowflakeLayout(t *testing.T) {
	view := snowflakeView()
	layout := ComputeLayout(view)
	inscribed := math.Cos(math.Pi / 6)
	stars, cores, spokes := map[string]Ring{}, map[string]Ring{}, map[string]Ring{}
	var composition *Ring
	for i, ring := range layout.Rings {
		switch ring.Layer {
		case ringStar:
			stars[ring.Star] = ring
		case graph.LayerCore:
			cores[ring.Star] = ring
		case graph.LayerSpoke:
			spokes[ring.Spoke] = ring
		case graph.LayerComposition:
			composition = &layout.Rings[i]
		}
	}

	if len(stars) != 3 || len(cores) != 3 || stars["internal"].Label != "internal" || stars["internal/blocks"].Label != "blocks" || cores["internal/gate"].Label != "core" || spokes["internal/web"].Label != "web" {
		t.Fatalf("a snowflake draws each star with its core and leaves: %+v", layout.Rings)
	}

	if composition == nil || !composition.Left || composition.Radius <= stars["internal"].Radius {
		t.Fatalf("composition is a half ring left of the root star: %+v", layout.Rings)
	}

	// Rings come cores first and stars last, so the page, drawing them in
	// reverse, paints every core over its star.
	order := map[string]int{graph.LayerCore: 0, graph.LayerSpoke: 1, ringStar: 2, graph.LayerComposition: 3}
	for i := 1; i < len(layout.Rings); i++ {
		if order[layout.Rings[i].Layer] < order[layout.Rings[i-1].Layer] {
			t.Fatalf("rings are out of order at %d: %+v", i, layout.Rings)
		}
	}

	within := func(x, y, r float64, ring Ring) bool {
		return math.Hypot(x-ring.X, y-ring.Y)+r <= ring.Radius*inscribed+0.5
	}

	for _, module := range view.Modules {
		p, placed := layout.Modules[module.ID]
		if !placed {
			t.Fatalf("%s was not placed", module.ID)
		}

		size := layout.Sizes[module.ID]
		switch module.Layer {
		case graph.LayerCore:
			if !within(p.X, p.Y, size, cores[module.Star]) {
				t.Errorf("%s lies outside the core of %s: %+v in %+v", module.ID, module.Star, p, cores[module.Star])
			}
		case graph.LayerSpoke:
			if !within(p.X, p.Y, size, spokes[module.Spoke]) {
				t.Errorf("%s lies outside spoke %s: %+v in %+v", module.ID, module.Spoke, p, spokes[module.Spoke])
			}
		case graph.LayerComposition:
			if p.X >= 0 || math.Hypot(p.X, p.Y) < stars["internal"].Radius*inscribed || !within(p.X, p.Y, size, *composition) {
				t.Errorf("composition %s is not in its half ring: %+v", module.ID, p)
			}
		case graph.LayerUnclassified:
			if p.Y <= 0 || math.Hypot(p.X, p.Y) < composition.Radius*inscribed {
				t.Errorf("unclassified %s is not outside along the bottom: %+v", module.ID, p)
			}
		}
	}

	// A star's spokes are its leaves and the stars of its branches. None
	// overlaps a sibling or the star's core, and each lies inside its star.
	children := map[string][]Ring{}
	for _, ring := range spokes {
		children[ring.Star] = append(children[ring.Star], ring)
	}

	for _, node := range view.Stars {
		if node.Parent != "" {
			children[node.Parent] = append(children[node.Parent], stars[node.Dir])
		}
	}

	for star, rings := range children {
		for i, a := range rings {
			if !within(a.X, a.Y, a.Radius, stars[star]) {
				t.Errorf("%s%s lies outside star %s", a.Spoke, a.Star, star)
			}

			if core := cores[star]; math.Hypot(a.X-core.X, a.Y-core.Y) < a.Radius+core.Radius {
				t.Errorf("%s%s overlaps the core of %s", a.Spoke, a.Star, star)
			}

			for _, b := range rings[i+1:] {
				if math.Hypot(a.X-b.X, a.Y-b.Y) < a.Radius+b.Radius {
					t.Errorf("sibling spokes %s%s and %s%s of %s overlap", a.Spoke, a.Star, b.Spoke, b.Star, star)
				}
			}
		}
	}

	for i, a := range view.Modules {
		for _, b := range view.Modules[i+1:] {
			pa, pb := layout.Modules[a.ID], layout.Modules[b.ID]
			if distance := math.Hypot(pa.X-pb.X, pa.Y-pb.Y); distance < layout.Sizes[a.ID]+layout.Sizes[b.ID] {
				t.Errorf("%s and %s overlap: %.1f apart", a.ID, b.ID, distance)
			}
		}
	}

	if again := ComputeLayout(snowflakeView()); !reflect.DeepEqual(layout, again) {
		t.Error("the snowflake layout is not deterministic")
	}

	// A new spoke takes its place in directory order without reordering the
	// others around the root.
	angles := func(layout Layout) (result []string) {
		root := stars["internal"]
		var rings []Ring
		for _, ring := range layout.Rings {
			if ring.Layer == graph.LayerSpoke && ring.Star == "internal" || ring.Layer == ringStar && ring.Star != "internal" && path.Dir(ring.Star) == "internal" {
				rings = append(rings, ring)
			}
		}

		turn := func(ring Ring) float64 {
			return math.Mod(math.Atan2(ring.Y-root.Y, ring.X-root.X)+math.Pi/2+2*math.Pi, 2*math.Pi)
		}

		slices.SortFunc(rings, func(a, b Ring) int { return cmp.Compare(turn(a), turn(b)) })
		for _, ring := range rings {
			result = append(result, cmp.Or(ring.Spoke, ring.Star))
		}

		return result
	}

	before := angles(layout)
	after := slices.DeleteFunc(angles(ComputeLayout(snowflakeView("internal/zebra"))), func(name string) bool { return name == "internal/zebra" })
	if !slices.Equal(before, after) || !slices.IsSorted(before) {
		t.Errorf("a new spoke reorders the others: %q, then %q", before, after)
	}
}

// TestSnowflakeLayoutKeepsUnlistedStars draws a module whose star the view's
// tree lacks, such as a planned one, rather than dropping it.
func TestSnowflakeLayoutKeepsUnlistedStars(t *testing.T) {
	layout := ComputeLayout(app.MapView{Architecture: rules.StyleSnowflake, Modules: []app.MapModule{
		{ID: "go:internal", Path: "internal", Layer: graph.LayerCore, Star: "internal"},
		{ID: "go:internal/web", Path: "internal/web", Layer: graph.LayerSpoke, Star: "internal", Spoke: "internal/web"},
		{ID: "go:internal/blocks/www", Path: "internal/blocks/www", Layer: graph.LayerSpoke, Star: "internal/blocks", Spoke: "internal/blocks/www"},
	}})

	if len(layout.Modules) != 3 {
		t.Fatalf("every module is placed: %+v", layout.Modules)
	}

	branch := 0
	for _, ring := range layout.Rings {
		if ring.Layer == ringStar && ring.Star == "internal/blocks" {
			branch++
		}
	}

	if branch != 1 {
		t.Fatalf("the unlisted star internal/blocks is drawn as a branch: %+v", layout.Rings)
	}
}
