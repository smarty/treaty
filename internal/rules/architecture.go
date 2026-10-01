package rules

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	StyleClean     = "clean"
	StyleHexagonal = "hexagonal"
	StyleLayered   = "layered"
	StyleModular   = "modular"
	StyleNone      = "none"
	StyleSlices    = "slices"
)

var (
	// CleanLayers are the rings of the clean architecture, innermost first.
	CleanLayers = []string{"entities", "use_cases", "interface_adapters", "frameworks"}

	ErrArchitecture = errors.New("invalid architecture")

	// HexagonalLayers are the layers of the hexagonal architecture,
	// innermost first.
	HexagonalLayers = []string{graph.LayerDomain, graph.LayerApplication, graph.LayerAdapter}

	// Styles lists every supported architecture, the default first.
	Styles = []string{StyleNone, StyleClean, StyleHexagonal, StyleLayered, StyleModular, StyleSlices}
)

// Architecture is the dependency rules of one codebase: a style, and the
// globs that place each module. Every style shares two rules: composition may
// depend on everything, and nothing may depend on composition. Unclassified
// modules are reported on their own and never checked.
//
// Notes:
//   - The none style is no architecture at all, and the default: every
//     module is placed in none, and every dependency is allowed. Contracts
//     and their changes are still tracked.
//   - Layers run innermost, or lowest, first. A layer may depend on itself
//     and on every layer inside or below it, never outward or upward.
//     Hexagonal adds one rule: an adapter may not depend on another adapter.
//   - Slices and contexts are directory roots, one per match of a glob in
//     Slices, such as internal/features/*. For the slices style, Layers are
//     optional and apply inside every slice, with globs relative to its root.
//   - Shared code may be used by every slice or context, and may itself use
//     only shared code.
type Architecture struct {
	Style       string
	Composition []string
	Layers      []Layer
	Shared      []string
	Slices      []string
	Public      []string
}

// Layer is one ring or band: its name, the globs of its modules and, for a
// hexagonal adapter, its side.
type Layer struct {
	Name  string
	Globs []string
	Side  string
}

// Placement is where the architecture puts one module.
type Placement struct {
	Layer  string
	Side   string
	Slice  string
	Public bool
}

// Violation is a module edge that breaks a rule of the architecture. It has
// at least one reference or import.
type Violation struct {
	Kind       string         `json:"kind"`
	Rule       string         `json:"rule"`
	From       string         `json:"from"`
	FromLayer  string         `json:"from_layer"`
	To         string         `json:"to"`
	ToLayer    string         `json:"to_layer"`
	References []graph.Edge   `json:"references"`
	Imports    []graph.Import `json:"imports,omitempty"`
}

// First locates the violation's first reference, or its first import when
// nothing the import provides is referenced.
//
// Returns:
//   - file: the file of the reference or import.
//   - line: its line.
//   - detail: what depends on what there, such as a → b or an import of b.
func (this Violation) First() (file string, line int, detail string) {
	if len(this.References) > 0 {
		ref := this.References[0]
		return ref.File, ref.Line, fmt.Sprintf("%s → %s", ref.From, ref.To)
	}

	if len(this.Imports) > 0 {
		item := this.Imports[0]
		return item.File, item.Line, fmt.Sprintf("an import of %s with no references", item.To)
	}

	return "", 0, ""
}

// Assign places every module of the graph, using the first glob that matches
// its path. Modules that match nothing become Unclassified.
//
// Parameters:
//   - g: the graph whose modules are placed.
func (this Architecture) Assign(g *graph.Graph) {
	for _, module := range g.Modules {
		placement := this.Resolve(module.Path)
		module.Layer, module.Side, module.Slice, module.Public = placement.Layer, placement.Side, placement.Slice, placement.Public
	}
}

// Check reports whether a module placed at from may depend on one placed at
// to. Callers skip a module's dependencies on itself.
//
// Parameters:
//   - from: the depending module's placement.
//   - to: the placement of the module depended on.
//
// Returns:
//   - allowed: true when the dependency follows the rules.
//   - rule: the rule it breaks, empty when allowed.
func (this Architecture) Check(from, to Placement) (allowed bool, rule string) {
	if this.Style == StyleNone {
		return true, ""
	}

	if to.Layer == graph.LayerComposition && from.Layer != graph.LayerComposition && from.Layer != graph.LayerUnclassified {
		return false, fmt.Sprintf("%s may not depend on composition: nothing may depend on the composition root", this.name(from))
	}

	if from.Layer == graph.LayerUnclassified || to.Layer == graph.LayerUnclassified || from.Layer == graph.LayerComposition {
		return true, ""
	}

	switch this.Style {
	case StyleSlices:
		return this.checkSlices(from, to)
	case StyleModular:
		return this.checkModular(from, to)
	case StyleHexagonal:
		if from.Layer == graph.LayerAdapter && to.Layer == graph.LayerAdapter {
			return false, "adapter may not depend on adapter: move the shared code inward, or wire the two together in composition"
		}
	}

	if this.rank(to.Layer) > this.rank(from.Layer) {
		return false, fmt.Sprintf("%s may not depend on %s: %s", from.Layer, to.Layer, this.direction())
	}

	return true, ""
}

// Guidance describes, for people, what a module placed here may do.
//
// Parameters:
//   - placement: the module's placement.
//
// Returns:
//   - result: one or two sentences.
func (this Architecture) Guidance(placement Placement) string {
	switch placement.Layer {
	case graph.LayerNone:
		return "No architecture: treaty.yaml declares none, so every dependency is allowed. Choose one with treaty init --architecture <name>."
	case graph.LayerComposition:
		if this.Style == StyleHexagonal {
			return "Composition: wires adapters into the core. The only layer allowed to use adapters; nothing may use it."
		}

		return "Composition: wires everything together. It may use every module; nothing may use it."
	case graph.LayerUnclassified:
		return "Unclassified: add a glob to treaty.yaml so its dependencies are checked."
	case graph.LayerShared:
		return "Shared: every slice or context may use it. It may use only shared code."
	case graph.LayerContext:
		if placement.Public {
			return fmt.Sprintf("Public API of the %s context: other contexts may use it. Keep it small and stable.", placement.Slice)
		}

		return fmt.Sprintf("Internal to the %s context: only %s may use it. It may use the public packages of other contexts and shared code.", placement.Slice, placement.Slice)
	case graph.LayerSlice:
		return fmt.Sprintf("Slice %s: may use itself and shared code, never another slice.", placement.Slice)
	}

	if this.Style == StyleHexagonal {
		switch placement.Layer {
		case graph.LayerDomain:
			return "Domain: depends only on domain. Keep instability near 0; others build on it."
		case graph.LayerApplication:
			return "Application: may use domain and application. Ports here should be abstract."
		case graph.LayerAdapter:
			return "Adapter: may use domain and application, never another adapter. Instability near 1 is expected."
		}
	}

	uses := strings.Join(this.MayUse(placement), ", ")
	if this.Style == StyleSlices {
		return fmt.Sprintf("%s layer of slice %s: may use %s, never another slice.", placement.Layer, placement.Slice, uses)
	}

	return fmt.Sprintf("%s: may use %s.", placement.Layer, uses)
}

// LayerNames lists the distinct layer names, innermost or lowest first.
//
// Returns:
//   - result: the names; empty for the modular style.
func (this Architecture) LayerNames() []string {
	var result []string
	for _, layer := range this.Layers {
		if !slices.Contains(result, layer.Name) {
			result = append(result, layer.Name)
		}
	}

	return result
}

// MayUse lists what a module placed here may depend on, for people and
// agents.
//
// Parameters:
//   - placement: the module's placement.
//
// Returns:
//   - result: layer names, or descriptions of slices and contexts.
func (this Architecture) MayUse(placement Placement) []string {
	names := this.LayerNames()
	sliced := this.Style == StyleSlices || this.Style == StyleModular
	switch placement.Layer {
	case graph.LayerNone:
		return []string{"every module"}
	case graph.LayerComposition:
		if sliced {
			return []string{"every " + this.unit(), graph.LayerShared, graph.LayerComposition}
		}

		return append(names, graph.LayerComposition)
	case graph.LayerUnclassified:
		if sliced {
			return []string{"every " + this.unit(), graph.LayerShared}
		}

		return names
	case graph.LayerShared:
		return []string{graph.LayerShared}
	}

	switch this.Style {
	case StyleModular:
		return []string{placement.Slice, "public packages of other contexts", graph.LayerShared}
	case StyleSlices:
		if placement.Layer == graph.LayerSlice {
			return []string{placement.Slice, graph.LayerShared}
		}

		return []string{fmt.Sprintf("%s (%s)", placement.Slice, strings.Join(this.within(placement.Layer), ", ")), graph.LayerShared}
	case StyleHexagonal:
		if placement.Layer == graph.LayerAdapter {
			return []string{graph.LayerDomain, graph.LayerApplication}
		}
	}

	return this.within(placement.Layer)
}

// Resolve places one module path.
//
// Notes:
//   - For the layered, hexagonal and clean styles, a glob that is exactly
//     the module's path wins over every wildcard; otherwise composition
//     comes first, then the layers in order.
//
// Parameters:
//   - modulePath: the module path relative to the repository root.
//
// Returns:
//   - result: the placement; its layer is Unclassified when no glob matches.
func (this Architecture) Resolve(modulePath string) (result Placement) {
	if this.Style == StyleNone {
		return Placement{Layer: graph.LayerNone}
	}

	if this.Style != StyleSlices && this.Style != StyleModular {
		// A module named by its exact path is placed there whatever the
		// wildcards say, so one module can leave a broad glob.
		if slices.Contains(this.Composition, modulePath) {
			return Placement{Layer: graph.LayerComposition}
		}

		for _, layer := range this.Layers {
			if slices.Contains(layer.Globs, modulePath) {
				return Placement{Layer: layer.Name, Side: layer.Side}
			}
		}
	}

	if MatchAny(this.Composition, modulePath) {
		return Placement{Layer: graph.LayerComposition}
	}

	if this.Style != StyleSlices && this.Style != StyleModular {
		for _, layer := range this.Layers {
			if MatchAny(layer.Globs, modulePath) {
				return Placement{Layer: layer.Name, Side: layer.Side}
			}
		}

		return Placement{Layer: graph.LayerUnclassified}
	}

	if MatchAny(this.Shared, modulePath) {
		return Placement{Layer: graph.LayerShared}
	}

	root, relative, ok := sliceRoot(this.Slices, modulePath)
	if !ok {
		return Placement{Layer: graph.LayerUnclassified}
	}

	if this.Style == StyleModular {
		public := this.Public
		if len(public) == 0 {
			public = []string{"."}
		}

		return Placement{Layer: graph.LayerContext, Slice: root, Public: MatchAny(public, relative)}
	}

	if len(this.Layers) == 0 {
		return Placement{Layer: graph.LayerSlice, Slice: root}
	}

	for _, layer := range this.Layers {
		if MatchAny(layer.Globs, relative) {
			return Placement{Layer: layer.Name, Slice: root}
		}
	}

	return Placement{Layer: graph.LayerUnclassified, Slice: root}
}

// Summary states the architecture's rules in one paragraph, for an agent.
//
// Returns:
//   - result: the rules.
func (this Architecture) Summary() string {
	switch this.Style {
	case StyleNone:
		return "Rules: none. No architecture is declared, so every dependency is allowed; contracts and their changes are still tracked."
	case StyleHexagonal:
		return "Rules: domain may use domain; application and adapters may use domain and application; composition may use everything; nothing may use composition; adapters may not use each other."
	case StyleSlices:
		result := "Rules (vertical slices): a slice may use itself and shared code, never another slice; shared code may use only shared code; composition may use everything; nothing may use composition."
		if len(this.Layers) > 0 {
			result += fmt.Sprintf(" Inside a slice, layers run top to bottom as %s, and each may use itself and the layers below it.", strings.Join(this.topDown(), ", "))
		}

		return result
	case StyleModular:
		return "Rules (modular monolith): a context may use its own packages, the public packages of other contexts and shared code; contexts may not depend on each other in a cycle; shared code may use only shared code; composition may use everything; nothing may use composition."
	}

	if this.Style == StyleClean {
		return fmt.Sprintf("Rules (clean): rings run outside in as %s; each may use itself and every ring inside it; composition may use everything; nothing may use composition.", strings.Join(this.topDown(), ", "))
	}

	return fmt.Sprintf("Rules (layered): layers run top to bottom as %s; each may use itself and every layer below it; composition may use everything; nothing may use composition.", strings.Join(this.topDown(), ", "))
}

// Validate checks that the architecture is complete and consistent.
//
// Returns:
//   - err: the first problem found.
//
// Errors:
//   - ErrArchitecture: an unknown style, a layer name the style does not
//     allow, a missing slice or context glob, or a setting the style does
//     not use.
func (this Architecture) Validate() error {
	if !slices.Contains(Styles, this.Style) {
		return fmt.Errorf("%w: unknown architecture %q; use one of %s", ErrArchitecture, this.Style, strings.Join(Styles, ", "))
	}

	if this.Style == StyleNone && (len(this.Composition) > 0 || len(this.Layers) > 0 || len(this.Shared) > 0 || len(this.Slices) > 0 || len(this.Public) > 0) {
		return fmt.Errorf("%w: the none architecture has no composition, layers, shared code, slices or contexts; choose another architecture to use them", ErrArchitecture)
	}

	sliced := this.Style == StyleSlices || this.Style == StyleModular
	if !sliced && (len(this.Shared) > 0 || len(this.Slices) > 0 || len(this.Public) > 0) {
		return fmt.Errorf("%w: shared, slices, contexts and public apply only to the slices and modular architectures", ErrArchitecture)
	}

	if this.Style == StyleSlices && len(this.Public) > 0 {
		return fmt.Errorf("%w: public applies only to the modular architecture", ErrArchitecture)
	}

	seen := map[string]bool{}
	for _, layer := range this.Layers {
		if layer.Name == graph.LayerComposition || layer.Name == graph.LayerShared || layer.Name == graph.LayerUnclassified || layer.Name == graph.LayerContext || layer.Name == graph.LayerSlice || layer.Name == graph.LayerNone {
			return fmt.Errorf("%w: %q is a reserved name and cannot be a layer", ErrArchitecture, layer.Name)
		}

		if seen[layer.Name] && !(this.Style == StyleHexagonal && layer.Name == graph.LayerAdapter) {
			return fmt.Errorf("%w: layer %q is listed twice", ErrArchitecture, layer.Name)
		}

		seen[layer.Name] = true
	}

	switch this.Style {
	case StyleHexagonal:
		for _, layer := range this.Layers {
			if !slices.Contains(HexagonalLayers, layer.Name) {
				return fmt.Errorf("%w: the hexagonal architecture has no %q layer; use composition, domain, application and adapter", ErrArchitecture, layer.Name)
			}
		}
	case StyleClean:
		for _, layer := range this.Layers {
			if !slices.Contains(CleanLayers, layer.Name) {
				return fmt.Errorf("%w: the clean architecture has no %q layer; use %s", ErrArchitecture, layer.Name, strings.Join(CleanLayers, ", "))
			}
		}
	case StyleLayered:
		if len(this.Layers) == 0 {
			return fmt.Errorf("%w: the layered architecture needs at least one layer", ErrArchitecture)
		}
	case StyleModular:
		if len(this.Layers) > 0 {
			return fmt.Errorf("%w: the modular architecture has no layers; list contexts instead", ErrArchitecture)
		}
	}

	if sliced && len(this.Slices) == 0 {
		return fmt.Errorf("%w: the %s architecture needs at least one %s glob", ErrArchitecture, this.Style, map[bool]string{true: "contexts", false: "slices"}[this.Style == StyleModular])
	}

	for _, glob := range this.Slices {
		if slices.Contains(strings.Split(glob, "/"), "**") {
			return fmt.Errorf("%w: %s glob %q may not use **: each match must be one directory", ErrArchitecture, this.unit(), glob)
		}
	}

	return nil
}

// Violations finds every module edge that breaks a rule of the
// architecture. For the modular style, it also finds every edge between
// contexts that closes a cycle, unless the edge already breaks a rule.
//
// Parameters:
//   - g: a graph whose modules have been placed.
//
// Returns:
//   - result: the violations, sorted by source and target.
func (this Architecture) Violations(g *graph.Graph) []Violation {
	var result []Violation
	for _, edge := range g.ModuleEdges() {
		from, to := g.Module(edge.From), g.Module(edge.To)
		if allowed, rule := this.Check(placementOf(from), placementOf(to)); !allowed {
			result = append(result, Violation{
				Kind: FindingLayerViolation, Rule: rule,
				From: edge.From, FromLayer: from.Layer,
				To: edge.To, ToLayer: to.Layer,
				References: edge.References, Imports: edge.Imports,
			})
		}
	}

	if this.Style == StyleModular {
		reported := map[[2]string]bool{}
		for _, violation := range result {
			reported[[2]string{violation.From, violation.To}] = true
		}

		for _, cycle := range contextCycles(g) {
			if !reported[[2]string{cycle.From, cycle.To}] {
				result = append(result, cycle)
			}
		}

		sort.SliceStable(result, func(i, j int) bool {
			if result[i].From != result[j].From {
				return result[i].From < result[j].From
			}

			return result[i].To < result[j].To
		})
	}

	return result
}

// checkModular applies the modular monolith's rules between two placed
// modules, neither of them composition or unclassified.
func (this Architecture) checkModular(from, to Placement) (bool, string) {
	switch {
	case to.Layer == graph.LayerShared:
		return true, ""
	case from.Layer == graph.LayerShared:
		return false, fmt.Sprintf("shared may not depend on the %s context: shared code may use only shared code", to.Slice)
	case from.Slice == to.Slice || to.Public:
		return true, ""
	default:
		return false, fmt.Sprintf("%s may not use the internals of %s: other contexts may use only its public packages", from.Slice, to.Slice)
	}
}

// checkSlices applies the vertical slices' rules between two placed
// modules, neither of them composition or unclassified.
func (this Architecture) checkSlices(from, to Placement) (bool, string) {
	switch {
	case to.Layer == graph.LayerShared:
		return true, ""
	case from.Layer == graph.LayerShared:
		return false, fmt.Sprintf("shared may not depend on slice %s: shared code may use only shared code", to.Slice)
	case from.Slice != to.Slice:
		return false, fmt.Sprintf("slice %s may not depend on slice %s: slices are independent; move the shared code into shared", from.Slice, to.Slice)
	case this.rank(to.Layer) > this.rank(from.Layer):
		return false, fmt.Sprintf("in slice %s, %s may not depend on %s: a layer may use only itself and the layers below it", from.Slice, from.Layer, to.Layer)
	default:
		return true, ""
	}
}

// direction says which way dependencies must point, for rule text.
func (this Architecture) direction() string {
	if this.Style == StyleLayered {
		return "a layer may use only itself and the layers below it"
	}

	return "dependencies point inward"
}

// name describes a placement for rule text.
func (this Architecture) name(placement Placement) string {
	if placement.Slice != "" && placement.Layer != graph.LayerShared {
		return placement.Slice
	}

	return placement.Layer
}

// rank orders a layer from the innermost, or lowest, outward. A name that is
// not a layer ranks outside every layer.
func (this Architecture) rank(layer string) int {
	if index := slices.Index(this.LayerNames(), layer); index >= 0 {
		return index
	}

	return len(this.Layers)
}

// topDown lists the layer names outermost, or highest, first.
func (this Architecture) topDown() []string {
	result := this.LayerNames()
	slices.Reverse(result)
	return result
}

// unit names what the style divides code into.
func (this Architecture) unit() string {
	if this.Style == StyleModular {
		return "context"
	}

	return "slice"
}

// within lists a layer and every layer inside or below it, innermost first.
func (this Architecture) within(layer string) []string {
	names := this.LayerNames()
	return names[:min(this.rank(layer)+1, len(names))]
}

// contextCycles finds every module edge between two contexts that depend on
// each other, directly or through other contexts.
func contextCycles(g *graph.Graph) []Violation {
	type crossing struct {
		edge     graph.ModuleEdge
		from, to *graph.Module
	}

	next := map[string][]string{}
	var crossings []crossing
	for _, edge := range g.ModuleEdges() {
		from, to := g.Module(edge.From), g.Module(edge.To)
		if from.Layer != graph.LayerContext || to.Layer != graph.LayerContext || from.Slice == to.Slice {
			continue
		}

		if !slices.Contains(next[from.Slice], to.Slice) {
			next[from.Slice] = append(next[from.Slice], to.Slice)
		}

		crossings = append(crossings, crossing{edge, from, to})
	}

	var result []Violation
	for _, each := range crossings {
		back := contextPath(next, each.to.Slice, each.from.Slice)
		if back == nil {
			continue
		}

		result = append(result, Violation{
			Kind:       FindingCycle,
			Rule:       fmt.Sprintf("contexts may not depend on each other in a cycle: %s → %s", each.from.Slice, strings.Join(back, " → ")),
			From:       each.edge.From,
			FromLayer:  each.from.Layer,
			To:         each.edge.To,
			ToLayer:    each.to.Layer,
			References: each.edge.References,
			Imports:    each.edge.Imports,
		})
	}

	return result
}

// contextPath finds the shortest path of contexts from start to goal,
// including both, or nil when goal is unreachable.
func contextPath(next map[string][]string, start, goal string) []string {
	previous := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == goal {
			var result []string
			for at := goal; at != ""; at = previous[at] {
				result = append([]string{at}, result...)
			}

			return result
		}

		targets := slices.Clone(next[current])
		sort.Strings(targets)
		for _, target := range targets {
			if _, seen := previous[target]; !seen {
				previous[target] = current
				queue = append(queue, target)
			}
		}
	}

	return nil
}

func placementOf(module *graph.Module) Placement {
	return Placement{Layer: module.Layer, Side: module.Side, Slice: module.Slice, Public: module.Public}
}

// sliceRoot finds the slice or context holding a module path: the prefix of
// the path that matches one of the globs, and the rest of the path inside
// it, which is "." for the root package itself.
func sliceRoot(globs []string, modulePath string) (root, relative string, ok bool) {
	segments := strings.Split(modulePath, "/")
	for _, glob := range globs {
		count := len(strings.Split(glob, "/"))
		if len(segments) < count {
			continue
		}

		candidate := strings.Join(segments[:count], "/")
		if !Match(glob, candidate) {
			continue
		}

		relative = path.Join(segments[count:]...)
		if relative == "" {
			relative = "."
		}

		return candidate, relative, true
	}

	return "", "", false
}
