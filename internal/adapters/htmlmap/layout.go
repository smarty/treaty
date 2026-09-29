package htmlmap

import (
	"math"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
)

const (
	groupLabel  = 16.0
	groupPad    = 10.0
	moduleSize  = 34.0
	moduleSpace = moduleSize * 1.35
	ringGap     = 18.0
	siblingGap  = 16.0
)

// Group is a directory drawn as a hexagon around the modules beneath it.
// Groups are visual only: rules and metrics stay per module.
type Group struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Layer   string   `json:"layer"`
	Parent  string   `json:"parent,omitempty"`
	Depth   int      `json:"depth"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	Radius  float64  `json:"radius"`
	Modules []string `json:"modules"`
}

// Layout is the computed geometry of the map.
type Layout struct {
	Modules map[string]Point `json:"modules"`
	Groups  []Group          `json:"groups"`
	Rings   []Ring           `json:"rings"`
	Size    float64          `json:"size"`
	Extent  float64          `json:"extent"`
}

// Point is a position on the map.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Ring is one layer's hexagon, from the center outward.
type Ring struct {
	Layer  string  `json:"layer"`
	Radius float64 `json:"radius"`
}

// item is one thing packed into a group or ring: a module or a group.
type item struct {
	module string
	path   string
	items  []*item
	radius float64
	x, y   float64
}

// node is one directory in a layer's path tree.
type node struct {
	path     string
	module   string
	children map[string]*node
}

// ComputeLayout places modules in concentric layer rings: domain in the
// center, application around it, driving adapters on the left and driven
// adapters on the right of the outer ring, and unclassified modules along
// the bottom outside it. Composition modules share the left of the adapter
// ring with driving adapters, at its far left: they are the only modules
// allowed to depend on adapters.
//
// Notes:
//   - Within each ring, directories with two or more children become groups:
//     hexagons that hold their packages and sub-groups. A directory with one
//     child is skipped, a package with sub-packages is a group whose center
//     is the package itself, and a group that would hold a ring's entire
//     contents is unwrapped onto the ring.
//   - Sizes are computed bottom-up and ring radii from them, so the layout is
//     the fully expanded one. Collapsing a group never moves anything.
//
// Parameters:
//   - modules: the modules to place.
//
// Returns:
//   - result: module and group positions, and ring radii.
func ComputeLayout(modules []app.MapModule) (result Layout) {
	buckets := map[string][]app.MapModule{}
	for _, module := range modules {
		key := module.Layer
		if module.Layer == graph.LayerAdapter {
			key = module.Side
		}

		buckets[key] = append(buckets[key], module)
	}

	result = Layout{Modules: map[string]Point{}, Size: moduleSize}
	top := map[string][]*item{}
	for key, members := range buckets {
		top[key] = topItems(members)
	}

	domain := top[graph.LayerDomain]
	radius := 0.0
	if len(domain) > 1 {
		radius = arcRadius(domain, 0, 2*math.Pi, true)
	}

	arrange(domain, radius, 0, 2*math.Pi, true)
	boundary := hexRadius(extent(domain, radius) + ringGap)
	result.Rings = append(result.Rings, Ring{Layer: graph.LayerDomain, Radius: boundary})

	application := top[graph.LayerApplication]
	radius = math.Max(boundary+largest(application)+ringGap, arcRadius(application, 0, 2*math.Pi, true))
	arrange(application, radius, -math.Pi/2, 2*math.Pi, true)
	boundary = hexRadius(math.Max(radius+largest(application), boundary) + ringGap)
	result.Rings = append(result.Rings, Ring{Layer: graph.LayerApplication, Radius: boundary})

	driving, driven := top[graph.SideDriving], top[graph.SideDriven]
	half := len(driving) / 2
	left := append(append(append([]*item{}, driving[:half]...), top[graph.LayerComposition]...), driving[half:]...)
	arc := math.Pi * 0.75
	biggest := math.Max(largest(left), largest(driven))
	radius = math.Max(boundary+biggest+ringGap, math.Max(arcRadius(left, math.Pi-arc/2, arc, false), arcRadius(driven, -arc/2, arc, false)))
	arrange(left, radius, math.Pi-arc/2, arc, false)
	arrange(driven, radius, -arc/2, arc, false)
	boundary = hexRadius(math.Max(radius+biggest, boundary) + ringGap)
	result.Rings = append(result.Rings, Ring{Layer: graph.LayerAdapter, Radius: boundary})

	result.Extent = boundary + moduleSize
	if unclassified := top[graph.LayerUnclassified]; len(unclassified) > 0 {
		radius = math.Max(boundary+largest(unclassified)+ringGap, arcRadius(unclassified, math.Pi/4, math.Pi/2, false))
		arrange(unclassified, radius, math.Pi/4, math.Pi/2, false)
		result.Extent = math.Max(result.Extent, radius+largest(unclassified)+moduleSize)
	}

	for _, key := range []string{graph.LayerDomain, graph.LayerApplication, graph.SideDriving, graph.SideDriven, graph.LayerComposition, graph.LayerUnclassified} {
		layer := key
		if key == graph.SideDriving || key == graph.SideDriven {
			layer = graph.LayerAdapter
		}

		for _, placed := range top[key] {
			emit(&result, placed, 0, 0, key, layer, "", 0)
		}
	}

	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Depth != result.Groups[j].Depth {
			return result.Groups[i].Depth < result.Groups[j].Depth
		}

		return result.Groups[i].ID < result.Groups[j].ID
	})
	return result
}

// arcRadius finds the smallest radius at which the items, spread along an
// arc in proportion to their size, leave every neighbor at least siblingGap
// apart.
func arcRadius(items []*item, start, arc float64, wrap bool) float64 {
	if len(items) == 0 {
		return 0
	}

	total := 0.0
	for _, placed := range items {
		total += 2*placed.radius + siblingGap
	}

	radius := total / arc
	for range 200 {
		arrange(items, radius, start, arc, wrap)
		if !crowded(items, wrap) {
			return radius
		}

		radius *= 1.05
	}

	return radius
}

// arrange places items along an arc, each taking a share of the angle in
// proportion to its size. A single item on a full circle sits at the center.
func arrange(items []*item, radius, start, arc float64, wrap bool) {
	if len(items) == 1 && wrap && radius == 0 {
		items[0].x, items[0].y = 0, 0
		return
	}

	total := 0.0
	for _, placed := range items {
		total += 2*placed.radius + siblingGap
	}

	cursor := start
	for _, placed := range items {
		share := arc * (2*placed.radius + siblingGap) / total
		angle := cursor + share/2
		placed.x, placed.y = radius*math.Cos(angle), radius*math.Sin(angle)
		cursor += share
	}
}

func crowded(items []*item, wrap bool) bool {
	for i := range items {
		j := i + 1
		if j == len(items) {
			if !wrap || len(items) < 2 {
				continue
			}

			j = 0
		}

		a, b := items[i], items[j]
		if math.Hypot(a.x-b.x, a.y-b.y) < a.radius+b.radius+siblingGap {
			return true
		}
	}

	return false
}

func emit(result *Layout, placed *item, originX, originY float64, bucket, layer, parent string, depth int) {
	x, y := originX+placed.x, originY+placed.y
	if placed.module != "" {
		result.Modules[placed.module] = Point{X: round(x), Y: round(y)}
		return
	}

	id := bucket + ":" + placed.path
	result.Groups = append(result.Groups, Group{
		ID: id, Path: placed.path, Layer: layer, Parent: parent, Depth: depth,
		X: round(x), Y: round(y), Radius: round(placed.radius), Modules: members(placed),
	})
	for _, child := range placed.items {
		emit(result, child, x, y, bucket, layer, id, depth+1)
	}
}

func extent(items []*item, radius float64) float64 {
	if len(items) == 1 && radius == 0 {
		return items[0].radius
	}

	return radius + largest(items)
}

func hexRadius(inscribed float64) float64 {
	return inscribed / math.Cos(math.Pi/6)
}

// honeycomb returns n slots on a hexagonal grid with neighbors spacing
// apart: the center first, then ring after ring outward.
func honeycomb(n int, spacing float64) [][2]float64 {
	result := [][2]float64{{0, 0}}
	directions := [][2]int{{1, 0}, {0, 1}, {-1, 1}, {-1, 0}, {0, -1}, {1, -1}}
	for ring := 1; len(result) < n; ring++ {
		q, r := -ring, ring
		for side := 0; side < 6; side++ {
			for step := 0; step < ring; step++ {
				result = append(result, [2]float64{spacing * (float64(q) + float64(r)/2), spacing * float64(r) * math.Sqrt(3) / 2})
				q, r = q+directions[side][0], r+directions[side][1]
			}
		}
	}

	return result[:n]
}

func largest(items []*item) float64 {
	result := 0.0
	for _, placed := range items {
		result = math.Max(result, placed.radius)
	}

	return result
}

func members(placed *item) []string {
	if placed.module != "" {
		return []string{placed.module}
	}

	var result []string
	for _, child := range placed.items {
		result = append(result, members(child)...)
	}

	sort.Strings(result)
	return result
}

// pack lays out a group's items on a honeycomb, centers the cluster, and
// sizes the group's hexagon to hold it with room for its label.
func pack(group *item) {
	spacing := 2*largest(group.items) + siblingGap
	slots := honeycomb(len(group.items), spacing)
	centerX, centerY := 0.0, 0.0
	for i, placed := range group.items {
		placed.x, placed.y = slots[i][0], slots[i][1]
		centerX += placed.x / float64(len(group.items))
		centerY += placed.y / float64(len(group.items))
	}

	reach := 0.0
	for _, placed := range group.items {
		placed.x -= centerX
		placed.y -= centerY
		reach = math.Max(reach, math.Hypot(placed.x, placed.y)+placed.radius)
	}

	group.radius = hexRadius(reach+groupPad) + groupLabel
}

// represent turns a directory node into the item drawn for it: a module, a
// group, or, for a directory with one child and no package, that child.
func represent(current *node) *item {
	if len(current.children) == 0 {
		return &item{module: current.module, radius: moduleSpace}
	}

	if current.module == "" && len(current.children) == 1 {
		for _, child := range current.children {
			return represent(child)
		}
	}

	group := &item{path: current.path}
	if current.module != "" {
		group.items = append(group.items, &item{module: current.module, radius: moduleSpace})
	}

	for _, name := range sortedKeys(current.children) {
		group.items = append(group.items, represent(current.children[name]))
	}

	pack(group)
	return group
}

func round(value float64) float64 {
	return math.Round(value*10) / 10
}

func sortedKeys(children map[string]*node) []string {
	result := make([]string, 0, len(children))
	for name := range children {
		result = append(result, name)
	}

	sort.Strings(result)
	return result
}

// topItems builds the path tree of one ring's modules and returns what the
// ring places directly: its top-level modules and groups.
func topItems(modules []app.MapModule) []*item {
	root := &node{children: map[string]*node{}}
	for _, module := range modules {
		current := root
		for _, segment := range strings.Split(module.Path, "/") {
			child := current.children[segment]
			if child == nil {
				child = &node{path: strings.TrimPrefix(current.path+"/"+segment, "/"), children: map[string]*node{}}
				current.children[segment] = child
			}

			current = child
		}

		current.module = module.ID
	}

	var result []*item
	for _, name := range sortedKeys(root.children) {
		result = append(result, represent(root.children[name]))
	}

	// A group holding everything in the ring says nothing the ring does not,
	// and a lone item on a ring leaves the rest of it empty. Unwrap it.
	for len(result) == 1 && result[0].module == "" {
		result = result[0].items
	}

	return result
}
