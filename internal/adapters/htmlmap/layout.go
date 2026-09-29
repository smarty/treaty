package htmlmap

import (
	"math"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const (
	groupLabel  = 16.0
	groupPad    = 10.0
	moduleSize  = 34.0
	moduleSpace = moduleSize * 1.35
	regionGap   = 24.0
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

// Label is text placed on the map, such as the side of an adapter ring.
type Label struct {
	Text string  `json:"text"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// Layout is the computed geometry of the map: rings for hexagonal and clean,
// regions for layered, slices and modular.
type Layout struct {
	Modules map[string]Point `json:"modules"`
	Groups  []Group          `json:"groups"`
	Rings   []Ring           `json:"rings"`
	Regions []Region         `json:"regions"`
	Labels  []Label          `json:"labels"`
	Size    float64          `json:"size"`
	Extent  float64          `json:"extent"`
}

// Point is a position on the map.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Region is a labeled rectangle: a band, a slice, a cell of the grid or a
// context. X and Y are its top-left corner, and Tone picks its shade.
type Region struct {
	Label string  `json:"label"`
	Tone  int     `json:"tone"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// Ring is one layer's hexagon, from the center outward. Tone picks its
// shade.
type Ring struct {
	Layer  string  `json:"layer"`
	Tone   int     `json:"tone"`
	Radius float64 `json:"radius"`
}

// box is a labeled rectangle of items flowed into rows.
type box struct {
	label  string
	tone   int
	bucket string
	layer  string
	items  []*item
	w, h   float64
}

// gridBlock is the middle of a region layout: slices as columns of cells,
// one per layer, or contexts as islands in rows.
type gridBlock struct {
	columns []string
	cells   [][]*box
	islands []*box
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

// fit sizes the box to a width, flowing its items into rows inside it.
func (this *box) fit(width float64) {
	this.w = width
	height := flow(this.items, width-2*groupPad)
	if height == 0 {
		height = moduleSpace
	}

	this.h = this.top() + height + groupPad
}

// natural is the width the box needs to hold its items in one row.
func (this *box) natural() float64 {
	total := 0.0
	for _, placed := range this.items {
		total += 2*placed.radius + siblingGap
	}

	return math.Max(total-siblingGap, 2*moduleSpace) + 2*groupPad
}

// place emits the box's items and its region with its top-left corner at
// x, y.
func (this *box) place(result *Layout, x, y float64) {
	if this.label != "" {
		result.Regions = append(result.Regions, Region{Label: this.label, Tone: this.tone, X: round(x), Y: round(y), W: round(this.w), H: round(this.h)})
	}

	for _, placed := range this.items {
		emit(result, placed, x+groupPad, y+this.top(), this.bucket, this.layer, "", 0)
	}
}

// top is the room above the items: the label, or padding alone.
func (this *box) top() float64 {
	if this.label == "" {
		return groupPad
	}

	return groupLabel + groupPad
}

// fit sizes every column or island, and returns the block's size.
func (this *gridBlock) fit() (width, height float64) {
	widest := 380.0
	narrowest := 2*moduleSpace + 2*groupPad
	if len(this.islands) > 0 {
		perRow := int(math.Ceil(math.Sqrt(float64(len(this.islands)))))
		for start := 0; start < len(this.islands); start += perRow {
			rowWidth, rowHeight := 0.0, 0.0
			for _, island := range this.islands[start:min(start+perRow, len(this.islands))] {
				island.fit(math.Min(math.Max(island.natural(), narrowest), widest))
				rowWidth += island.w + regionGap
				rowHeight = math.Max(rowHeight, island.h)
			}

			width = math.Max(width, rowWidth-regionGap)
			height += rowHeight + regionGap
		}

		return width, height - regionGap
	}

	if len(this.cells) == 0 {
		return 0, 0
	}

	rowHeights := make([]float64, len(this.cells[0]))
	for _, column := range this.cells {
		cellWidth := narrowest
		for _, cell := range column {
			cellWidth = math.Max(cellWidth, math.Min(cell.natural(), widest))
		}

		for r, cell := range column {
			cell.fit(cellWidth)
			rowHeights[r] = math.Max(rowHeights[r], cell.h)
		}

		width += cellWidth + 2*groupPad + regionGap
	}

	for _, column := range this.cells {
		for r, cell := range column {
			cell.h = rowHeights[r]
		}
	}

	for _, rowHeight := range rowHeights {
		height += rowHeight + groupPad
	}

	return width - regionGap, groupLabel + groupPad + height
}

// place emits the block with its top-left corner at x, y.
func (this *gridBlock) place(result *Layout, x, y float64) {
	if len(this.islands) > 0 {
		perRow := int(math.Ceil(math.Sqrt(float64(len(this.islands)))))
		for start := 0; start < len(this.islands); start += perRow {
			cursor, rowHeight := x, 0.0
			for _, island := range this.islands[start:min(start+perRow, len(this.islands))] {
				island.place(result, cursor, y)
				cursor += island.w + regionGap
				rowHeight = math.Max(rowHeight, island.h)
			}

			y += rowHeight + regionGap
		}

		return
	}

	for c, column := range this.cells {
		width := column[0].w + 2*groupPad
		height := groupLabel + groupPad
		for _, cell := range column {
			height += cell.h + groupPad
		}

		result.Regions = append(result.Regions, Region{Label: path.Base(this.columns[c]), Tone: c % 2, X: round(x), Y: round(y), W: round(width), H: round(height)})
		cursor := y + groupLabel + groupPad
		for _, cell := range column {
			cell.place(result, x+groupPad, cursor)
			cursor += cell.h + groupPad
		}

		x += width + regionGap
	}
}

// ComputeLayout places modules by the view's architecture: concentric rings
// for hexagonal and clean, horizontal bands for layered, a grid of slices by
// layer for slices, and islands for the contexts of a modular monolith.
//
// Notes:
//   - Within each ring or region, directories with two or more children
//     become groups: hexagons that hold their packages and sub-groups. A
//     directory with one child is skipped, a package with sub-packages is a
//     group whose center is the package itself, and a group that would hold
//     a ring's entire contents is unwrapped onto the ring.
//   - Sizes are computed bottom-up, so the layout is the fully expanded one.
//     Collapsing a group never moves anything.
//
// Parameters:
//   - view: the modules to place, with the architecture and its layers.
//
// Returns:
//   - result: module and group positions, rings or regions, and labels.
func ComputeLayout(view app.MapView) (result Layout) {
	switch view.Architecture {
	case rules.StyleLayered:
		return bandLayout(view)
	case rules.StyleSlices:
		return gridLayout(view)
	case rules.StyleModular:
		return islandLayout(view)
	case rules.StyleClean:
		return ringLayout(view.Modules, rules.CleanLayers, false)
	default:
		return ringLayout(view.Modules, rules.HexagonalLayers, true)
	}
}

// ringLayout places modules in concentric rings, innermost first, with
// unclassified modules along the bottom outside them. The outer ring is
// split in two: for hexagonal, driving adapters on the left and driven on
// the right; otherwise, its modules divided in half. Composition shares the
// left of the outer ring, at its far left.
func ringLayout(modules []app.MapModule, rings []string, sided bool) (result Layout) {
	outer := rings[len(rings)-1]
	buckets := map[string][]app.MapModule{}
	for _, module := range modules {
		key := module.Layer
		if sided && module.Layer == outer {
			key = module.Side
		}

		buckets[key] = append(buckets[key], module)
	}

	result = Layout{Modules: map[string]Point{}, Size: moduleSize}
	top := map[string][]*item{}
	for key, members := range buckets {
		top[key] = topItems(members)
	}

	boundary, radius := 0.0, 0.0
	for i, layer := range rings[:len(rings)-1] {
		members := top[layer]
		if i == 0 {
			if len(members) > 1 {
				radius = arcRadius(members, 0, 2*math.Pi, true)
			}

			arrange(members, radius, 0, 2*math.Pi, true)
			boundary = hexRadius(extent(members, radius) + ringGap)
		} else {
			radius = math.Max(boundary+largest(members)+ringGap, arcRadius(members, 0, 2*math.Pi, true))
			arrange(members, radius, -math.Pi/2, 2*math.Pi, true)
			boundary = hexRadius(math.Max(radius+largest(members), boundary) + ringGap)
		}

		result.Rings = append(result.Rings, Ring{Layer: layer, Tone: i, Radius: boundary})
	}

	leftKey, rightKey := graph.SideDriving, graph.SideDriven
	if !sided {
		all := top[outer]
		leftKey, rightKey = outer+":left", outer+":right"
		top[leftKey], top[rightKey] = all[:(len(all)+1)/2], all[(len(all)+1)/2:]
		delete(top, outer)
	}

	driving, driven := top[leftKey], top[rightKey]
	half := len(driving) / 2
	left := append(append(append([]*item{}, driving[:half]...), top[graph.LayerComposition]...), driving[half:]...)
	arc := math.Pi * 0.75
	biggest := math.Max(largest(left), largest(driven))
	radius = math.Max(boundary+biggest+ringGap, math.Max(arcRadius(left, math.Pi-arc/2, arc, false), arcRadius(driven, -arc/2, arc, false)))
	arrange(left, radius, math.Pi-arc/2, arc, false)
	arrange(driven, radius, -arc/2, arc, false)
	boundary = hexRadius(math.Max(radius+biggest, boundary) + ringGap)
	result.Rings = append(result.Rings, Ring{Layer: outer, Tone: len(rings) - 1, Radius: boundary})
	if sided {
		result.Labels = append(result.Labels, Label{Text: graph.SideDriving, X: round(-boundary * 0.72), Y: round(boundary * 0.95)}, Label{Text: graph.SideDriven, X: round(boundary * 0.72), Y: round(boundary * 0.95)})
	}

	result.Extent = boundary + moduleSize
	if unclassified := top[graph.LayerUnclassified]; len(unclassified) > 0 {
		radius = math.Max(boundary+largest(unclassified)+ringGap, arcRadius(unclassified, math.Pi/4, math.Pi/2, false))
		arrange(unclassified, radius, math.Pi/4, math.Pi/2, false)
		result.Extent = math.Max(result.Extent, radius+largest(unclassified)+moduleSize)
	}

	keys := append(slices.Clone(rings[:len(rings)-1]), leftKey, rightKey, graph.LayerComposition, graph.LayerUnclassified)
	for _, key := range keys {
		layer, _, _ := strings.Cut(key, ":")
		if key == graph.SideDriving || key == graph.SideDriven {
			layer = outer
		}

		for _, placed := range top[key] {
			emit(&result, placed, 0, 0, strings.TrimSuffix(strings.TrimSuffix(key, ":left"), ":right"), layer, "", 0)
		}
	}

	sortGroups(&result)
	return result
}

// bandLayout stacks the layers of a layered architecture as horizontal
// bands, the top layer highest, with composition above them and
// unclassified modules below.
func bandLayout(view app.MapView) Layout {
	rows := append([]string{graph.LayerComposition}, view.Layers...)
	slices.Reverse(rows[1:])
	rows = append(rows, graph.LayerUnclassified)
	byLayer := map[string][]app.MapModule{}
	for _, module := range view.Modules {
		byLayer[module.Layer] = append(byLayer[module.Layer], module)
	}

	var boxes []*box
	for i, layer := range rows {
		if len(byLayer[layer]) == 0 && (layer == graph.LayerComposition || layer == graph.LayerUnclassified) {
			continue
		}

		boxes = append(boxes, &box{label: layerLabel(layer), tone: i % 2, bucket: layer, layer: layer, items: topItems(byLayer[layer])})
	}

	return stack(boxes, nil)
}

// gridLayout draws each slice as a column and, when slices have layers,
// each layer as a row across them. Composition spans the top, and shared
// code and unclassified modules span the bottom.
func gridLayout(view app.MapView) Layout {
	rows := slices.Clone(view.Layers)
	slices.Reverse(rows)
	if len(rows) == 0 {
		rows = []string{graph.LayerSlice}
	}

	cells := map[[2]string][]app.MapModule{}
	byLayer := map[string][]app.MapModule{}
	var columns []string
	unplaced := false
	for _, module := range view.Modules {
		if module.Section == "" {
			byLayer[module.Layer] = append(byLayer[module.Layer], module)
			continue
		}

		if !slices.Contains(columns, module.Section) {
			columns = append(columns, module.Section)
		}

		if module.Layer == graph.LayerUnclassified {
			unplaced = true
		}

		key := [2]string{module.Section, module.Layer}
		cells[key] = append(cells[key], module)
	}

	sort.Strings(columns)
	if unplaced {
		rows = append(rows, graph.LayerUnclassified)
	}

	var grid [][]*box
	for _, column := range columns {
		var cellBoxes []*box
		for _, row := range rows {
			label := ""
			if row != graph.LayerSlice {
				label = layerLabel(row)
			}

			cellBoxes = append(cellBoxes, &box{label: label, tone: 2, bucket: column + "|" + row, layer: row, items: topItems(cells[[2]string{column, row}])})
		}

		grid = append(grid, cellBoxes)
	}

	var above, below []*box
	if members := byLayer[graph.LayerComposition]; len(members) > 0 {
		above = append(above, &box{label: "composition", tone: 1, bucket: graph.LayerComposition, layer: graph.LayerComposition, items: topItems(members)})
	}

	for _, layer := range []string{graph.LayerShared, graph.LayerUnclassified} {
		if members := byLayer[layer]; len(members) > 0 {
			below = append(below, &box{label: layer, tone: 1, bucket: layer, layer: layer, items: topItems(members)})
		}
	}

	return stack(above, &gridBlock{columns: columns, cells: grid}, below...)
}

// islandLayout draws each context of a modular monolith as an island,
// arranged in rows, with composition above them and shared code and
// unclassified modules below.
func islandLayout(view app.MapView) Layout {
	bySection := map[string][]app.MapModule{}
	byLayer := map[string][]app.MapModule{}
	var sections []string
	for _, module := range view.Modules {
		if module.Section == "" {
			byLayer[module.Layer] = append(byLayer[module.Layer], module)
			continue
		}

		if !slices.Contains(sections, module.Section) {
			sections = append(sections, module.Section)
		}

		bySection[module.Section] = append(bySection[module.Section], module)
	}

	sort.Strings(sections)
	var islands []*box
	for i, section := range sections {
		islands = append(islands, &box{label: path.Base(section), tone: i % 2, bucket: section, layer: graph.LayerContext, items: topItems(bySection[section])})
	}

	var above, below []*box
	if members := byLayer[graph.LayerComposition]; len(members) > 0 {
		above = append(above, &box{label: "composition", tone: 1, bucket: graph.LayerComposition, layer: graph.LayerComposition, items: topItems(members)})
	}

	for _, layer := range []string{graph.LayerShared, graph.LayerUnclassified} {
		if members := byLayer[layer]; len(members) > 0 {
			below = append(below, &box{label: layer, tone: 1, bucket: layer, layer: layer, items: topItems(members)})
		}
	}

	return stack(above, &gridBlock{islands: islands}, below...)
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

// flow places items left to right in rows no wider than width, each at its
// center relative to the top-left corner.
//
// Returns:
//   - height: the height the rows take, 0 when there are no items.
func flow(items []*item, width float64) (height float64) {
	x, y, rowHeight := 0.0, 0.0, 0.0
	for _, placed := range items {
		diameter := 2 * placed.radius
		if x > 0 && x+diameter > width {
			x, y, rowHeight = 0, y+rowHeight+siblingGap, 0
		}

		placed.x, placed.y = x+placed.radius, y+placed.radius
		x += diameter + siblingGap
		rowHeight = math.Max(rowHeight, diameter)
	}

	return y + rowHeight
}

// layerLabel turns a layer name such as use_cases into a label.
func layerLabel(layer string) string {
	return strings.ReplaceAll(layer, "_", " ")
}

func sortGroups(result *Layout) {
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Depth != result.Groups[j].Depth {
			return result.Groups[i].Depth < result.Groups[j].Depth
		}

		return result.Groups[i].ID < result.Groups[j].ID
	})
}

// stack lays out bands above a block and bands below it, all as wide as the
// widest, centered on the origin.
func stack(above []*box, block *gridBlock, below ...*box) (result Layout) {
	result = Layout{Modules: map[string]Point{}, Size: moduleSize}
	blockWidth, blockHeight := 0.0, 0.0
	if block != nil {
		blockWidth, blockHeight = block.fit()
	}

	bands := append(slices.Clone(above), below...)
	area, widest := 0.0, 0.0
	for _, band := range bands {
		widest = math.Max(widest, band.natural())
		for _, placed := range band.items {
			area += math.Pow(2*placed.radius+siblingGap, 2)
		}
	}

	width := math.Max(blockWidth, math.Min(widest, math.Max(900, 1.6*math.Sqrt(area))))
	width = math.Max(width, 4*moduleSpace)
	height := 0.0
	for _, band := range bands {
		band.fit(width)
		height += band.h + regionGap
	}

	if block != nil {
		height += blockHeight + regionGap
	}

	height -= regionGap
	x, y := -width/2, -height/2
	for _, band := range above {
		band.place(&result, x, y)
		y += band.h + regionGap
	}

	if block != nil {
		block.place(&result, -blockWidth/2, y)
		y += blockHeight + regionGap
	}

	for _, band := range below {
		band.place(&result, x, y)
		y += band.h + regionGap
	}

	// The margin keeps the top band's label clear of the legend.
	result.Extent = math.Max(width, height)/2 + moduleSize + 3*groupLabel
	sortGroups(&result)
	return result
}
