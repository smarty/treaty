package rules

import (
	"errors"
	"fmt"
	"math"
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
	StyleSnowflake = "snowflake"
	StyleVertical  = "vertical"
)

var (
	// CleanLayers are the rings of the clean architecture, innermost first.
	CleanLayers = []string{"entities", "use_cases", "interface_adapters", "frameworks"}

	ErrArchitecture = errors.New("invalid architecture")

	// HexagonalLayers are the layers of the hexagonal architecture,
	// innermost first.
	HexagonalLayers = []string{graph.LayerDomain, graph.LayerApplication, graph.LayerAdapter}

	// Styles lists every supported architecture, the default first.
	Styles = []string{StyleNone, StyleClean, StyleHexagonal, StyleLayered, StyleModular, StyleSnowflake, StyleVertical}
)

// Architecture is the dependency rules of one codebase: a style, and the
// globs that place each module. Every style but the snowflake shares two
// rules: composition may depend on everything, and nothing may depend on
// composition. Unclassified modules are reported on their own and never
// checked.
//
// Notes:
//   - The none style is no architecture at all, and the default: every
//     module is placed in none, and every dependency is allowed. Contracts
//     and their changes are still tracked.
//   - Layers run innermost, or lowest, first. A layer may depend on itself
//     and on every layer inside or below it, never outward or upward.
//     Hexagonal adds one rule: an adapter may not depend on another adapter.
//   - Boundaries are named groups of modules, for the vertical and modular
//     styles. For the vertical style, Layers are optional and apply inside
//     every boundary. Every glob, in boundaries, layers and public alike, is
//     relative to the repository root.
//   - Shared code may be used by every boundary, and may itself use only
//     shared code.
//   - Stars are the snowflake style's, in the order treaty.yaml lists
//     them, each a directory and the entries of its contracts and its
//     orchestrators: see Star. A snowflake has no composition: its
//     orchestrators do that work. Bind fits it to one tree's modules, so
//     placement can look through directories that hold none; copies share
//     the binding.
type Architecture struct {
	Style       string
	Composition []string
	Layers      []Layer
	Shared      []string
	Boundaries  []Boundary
	Public      []string
	Stars       []Star

	bound *starBinding
}

// Boundary is one named group of modules and the globs that gather them.
// Its globs may reach across directories, such as web/orders/** and
// store/orders/**.
type Boundary struct {
	Name  string
	Globs []string
}

// Layer is one ring or band: its name, the globs of its modules and, for a
// hexagonal adapter, its side.
type Layer struct {
	Name  string
	Globs []string
	Side  string
}

// Placement is where the architecture puts one module. For the snowflake
// style, Layer is contracts, orchestrator or leaf, Star is the directory of
// the star holding the module, Spoke is its leaf's directory, for a leaf,
// and Entry is the contracts or orchestrators entry that claims it, for a
// core package. An unclassified module has its star, and a misfit, one a
// lenient binding looks through, also its directory as Spoke.
type Placement struct {
	Layer     string
	Side      string
	Boundary  string
	Public    bool
	Ambiguous []string
	Star      string
	Spoke     string
	Entry     string
}

// Same reports whether two placements put a module in the same place.
//
// Parameters:
//   - other: the placement to compare with.
//
// Returns:
//   - result: true when layer, side, boundary, public, ambiguity, star,
//     spoke and entry match.
func (this Placement) Same(other Placement) bool {
	return this.Layer == other.Layer && this.Side == other.Side && this.Boundary == other.Boundary && this.Public == other.Public && slices.Equal(this.Ambiguous, other.Ambiguous) &&
		this.Star == other.Star && this.Spoke == other.Spoke && this.Entry == other.Entry
}

// Violation is a module edge that breaks a rule of the architecture. It has
// at least one reference or import. Existing marks one the base already
// had, which is shown but does not fail the check.
type Violation struct {
	Kind       string         `json:"kind"`
	Rule       string         `json:"rule"`
	Existing   bool           `json:"existing,omitempty"`
	From       string         `json:"from"`
	FromLayer  string         `json:"from_layer"`
	To         string         `json:"to"`
	ToLayer    string         `json:"to_layer"`
	References []graph.Edge   `json:"references"`
	Imports    []graph.Import `json:"imports,omitempty"`
}

// MarkExisting marks each violation the base also has, by its module edge,
// so a check fails only on violations a change introduced.
//
// Parameters:
//   - head: the violations in the working tree, marked in place.
//   - base: the violations in the base, under the same architecture.
func MarkExisting(head, base []Violation) {
	before := map[[2]string]bool{}
	for _, violation := range base {
		before[[2]string{violation.From, violation.To}] = true
	}

	for i := range head {
		head[i].Existing = before[[2]string{head[i].From, head[i].To}]
	}
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
// its path. Modules that match nothing become Unclassified. A snowflake is
// first bound to the graph's own modules, leniently, so a graph is never
// placed with another tree's directories.
//
// Parameters:
//   - g: the graph whose modules are placed.
func (this Architecture) Assign(g *graph.Graph) {
	paths := make([]string, 0, len(g.Modules))
	for _, module := range g.Modules {
		paths = append(paths, module.Path)
	}

	bound, _ := this.Bind(paths)
	for _, module := range g.Modules {
		placement := bound.Resolve(module.Path)
		module.Layer, module.Side, module.Boundary, module.Public, module.Ambiguous = placement.Layer, placement.Side, placement.Boundary, placement.Public, placement.Ambiguous
		module.Star, module.Spoke, module.CoreEntry = placement.Star, placement.Spoke, placement.Entry
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
	case StyleSnowflake:
		return this.checkSnowflake(from, to)
	case StyleVertical:
		return this.checkVertical(from, to)
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
	if this.Style == StyleSnowflake {
		return this.starGuidance(placement)
	}

	switch placement.Layer {
	case graph.LayerNone:
		return "No architecture: treaty.yaml declares none, so every dependency is allowed. Choose one with treaty init --architecture <name>."
	case graph.LayerComposition:
		if this.Style == StyleHexagonal {
			return "Composition: wires adapters into the core. The only layer allowed to use adapters; nothing may use it."
		}

		return "Composition: wires everything together. It may use every module; nothing may use it."
	case graph.LayerUnclassified:
		if len(placement.Ambiguous) > 0 {
			return fmt.Sprintf("Ambiguous: boundaries %s match it equally well, so it is in none of them. Name it exactly in one of them in treaty.yaml.", strings.Join(placement.Ambiguous, " and "))
		}

		return "Unclassified: add a glob to treaty.yaml so its dependencies are checked."
	case graph.LayerShared:
		return "Shared: every boundary may use it. It may use only shared code."
	case graph.LayerBoundary:
		if this.Style == StyleModular && placement.Public {
			return fmt.Sprintf("Public API of boundary %s: other boundaries may use it. Keep it small and stable.", placement.Boundary)
		}

		if this.Style == StyleModular {
			return fmt.Sprintf("Internal to boundary %s: only %s may use it. It may use the public packages of other boundaries and shared code.", placement.Boundary, placement.Boundary)
		}

		return fmt.Sprintf("Boundary %s: may use itself and shared code, never another boundary.", placement.Boundary)
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
	if this.Style == StyleVertical {
		return fmt.Sprintf("%s layer of boundary %s: may use %s, never another boundary.", placement.Layer, placement.Boundary, uses)
	}

	return fmt.Sprintf("%s: may use %s.", placement.Layer, uses)
}

// LayerNames lists the distinct layer names, innermost or lowest first.
//
// Returns:
//   - result: the names; empty for the modular and snowflake styles.
func (this Architecture) LayerNames() []string {
	if this.Style == StyleSnowflake {
		return nil
	}

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
//   - result: layer names, or descriptions of boundaries or cores.
func (this Architecture) MayUse(placement Placement) []string {
	if this.Style == StyleSnowflake {
		return this.starMayUse(placement)
	}

	names := this.LayerNames()
	sliced := this.bounded()
	switch placement.Layer {
	case graph.LayerNone:
		return []string{"every module"}
	case graph.LayerComposition:
		if sliced {
			return []string{"every boundary", graph.LayerShared, graph.LayerComposition}
		}

		return append(names, graph.LayerComposition)
	case graph.LayerUnclassified:
		if sliced {
			return []string{"every boundary", graph.LayerShared}
		}

		return names
	case graph.LayerShared:
		return []string{graph.LayerShared}
	}

	switch this.Style {
	case StyleModular:
		return []string{placement.Boundary, "public packages of other boundaries", graph.LayerShared}
	case StyleVertical:
		if placement.Layer == graph.LayerBoundary {
			return []string{placement.Boundary, graph.LayerShared}
		}

		return []string{fmt.Sprintf("%s (%s)", placement.Boundary, strings.Join(this.within(placement.Layer), ", ")), graph.LayerShared}
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
//   - For the vertical and modular styles, composition comes first, then
//     shared, then the boundaries. Of the boundaries that match, the one
//     whose glob is most specific wins: a glob that is exactly the path, or
//     else the one with the most literal segments. Boundaries that tie leave
//     the module unclassified, with the tied names in Ambiguous.
//   - For the modular style, a module is public when it matches a public
//     glob or, with none given, when it is at the top of its boundary: it
//     matches one of the boundary's globs without the trailing /**.
//   - For the snowflake style, see resolveSnowflake. An unbound snowflake
//     looks through every directory but its stars, its entries and the
//     path itself.
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

	if this.Style == StyleSnowflake {
		return this.resolveSnowflake(modulePath)
	}

	if !this.bounded() {
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

	if !this.bounded() {
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

	boundary, tied := this.boundaryOf(modulePath)
	if len(tied) > 0 {
		return Placement{Layer: graph.LayerUnclassified, Ambiguous: tied}
	}

	if boundary == nil {
		return Placement{Layer: graph.LayerUnclassified}
	}

	if this.Style == StyleModular {
		return Placement{Layer: graph.LayerBoundary, Boundary: boundary.Name, Public: this.public(*boundary, modulePath)}
	}

	if len(this.Layers) == 0 {
		return Placement{Layer: graph.LayerBoundary, Boundary: boundary.Name}
	}

	for _, layer := range this.Layers {
		if MatchAny(layer.Globs, modulePath) {
			return Placement{Layer: layer.Name, Boundary: boundary.Name}
		}
	}

	return Placement{Layer: graph.LayerUnclassified, Boundary: boundary.Name}
}

// Reason is a glob that places a module, at its key in treaty.yaml: Path
// is the key's path, such as layers, domain or boundaries, orders.
type Reason struct {
	Path []string `json:"path"`
	Glob string   `json:"glob"`
}

// Reasons says why Resolve places a module where it does: the glob that
// placed it, and for a vertical boundary with layers also the layer's. A
// module claimed equally by boundaries gets each one's glob. A module
// nothing places, or one with no architecture, has none.
//
// Notes:
//   - Composition may also be listed under layers for hexagonal; its path
//     is composition either way.
//   - For the snowflake style, a core package has the entry that claims
//     it, at stars, the star's directory and contracts or orchestrators. A
//     leaf is placed by its directory, so its reason has the star's key and
//     an empty glob.
//
// Parameters:
//   - modulePath: the module's path relative to the repository root.
//
// Returns:
//   - result: the globs that place it, outermost key first.
func (this Architecture) Reasons(modulePath string) (result []Reason) {
	placement := this.Resolve(modulePath)
	pick := func(globs []string) string {
		if slices.Contains(globs, modulePath) {
			return modulePath
		}

		for _, glob := range globs {
			if Match(glob, modulePath) {
				return glob
			}
		}

		return ""
	}

	boundaryGlob := func(boundary Boundary) string {
		best, result := -1, ""
		for _, glob := range boundary.Globs {
			if Match(glob, modulePath) && specificity(glob, modulePath) > best {
				best, result = specificity(glob, modulePath), glob
			}
		}

		return result
	}

	switch {
	case placement.Layer == graph.LayerNone:
		return nil
	case placement.Layer == graph.LayerComposition:
		return []Reason{{Path: []string{"composition"}, Glob: pick(this.Composition)}}
	case this.Style == StyleSnowflake:
		if placement.Layer == graph.LayerUnclassified {
			return nil
		}

		for _, role := range starRoles {
			if placement.Layer == role.layer {
				return []Reason{{Path: []string{"stars", placement.Star, role.list}, Glob: placement.Entry}}
			}
		}

		return []Reason{{Path: []string{"stars", placement.Star}, Glob: ""}}
	case placement.Layer == graph.LayerShared:
		return []Reason{{Path: []string{"shared"}, Glob: pick(this.Shared)}}
	case len(placement.Ambiguous) > 0:
		for _, boundary := range this.Boundaries {
			if slices.Contains(placement.Ambiguous, boundary.Name) {
				result = append(result, Reason{Path: []string{"boundaries", boundary.Name}, Glob: boundaryGlob(boundary)})
			}
		}

		return result
	}

	if placement.Boundary != "" {
		for _, boundary := range this.Boundaries {
			if boundary.Name == placement.Boundary {
				result = append(result, Reason{Path: []string{"boundaries", boundary.Name}, Glob: boundaryGlob(boundary)})
			}
		}
	}

	for _, layer := range this.Layers {
		if layer.Name != placement.Layer || layer.Side != placement.Side {
			continue
		}

		path := []string{"layers", layer.Name}
		if layer.Side != "" {
			path = append(path, layer.Side)
		}

		if glob := pick(layer.Globs); glob != "" {
			result = append(result, Reason{Path: path, Glob: glob})
		}
	}

	return result
}

// boundaryOf finds the boundary whose glob matches a module path most
// specifically, or the names of the boundaries that tie for it.
func (this Architecture) boundaryOf(modulePath string) (result *Boundary, tied []string) {
	best := -1
	for i, boundary := range this.Boundaries {
		score := -1
		for _, glob := range boundary.Globs {
			if Match(glob, modulePath) {
				score = max(score, specificity(glob, modulePath))
			}
		}

		switch {
		case score < 0 || score < best:
		case score > best:
			result, best, tied = &this.Boundaries[i], score, nil
		default:
			if tied == nil {
				tied = []string{result.Name}
			}

			tied = append(tied, boundary.Name)
		}
	}

	if tied != nil {
		return nil, tied
	}

	return result, nil
}

// public reports whether a module is one other boundaries may use.
func (this Architecture) public(boundary Boundary, modulePath string) bool {
	if len(this.Public) > 0 {
		return MatchAny(this.Public, modulePath)
	}

	for _, glob := range boundary.Globs {
		top, _ := strings.CutSuffix(glob, "/**")
		if Match(top, modulePath) {
			return true
		}
	}

	return false
}

// specificity scores how closely a glob that matches a path names it: one
// that is exactly the path beats every wildcard, and otherwise each literal
// segment counts one.
func specificity(glob, modulePath string) int {
	if glob == modulePath {
		return math.MaxInt
	}

	result := 0
	for _, segment := range strings.Split(glob, "/") {
		if !strings.ContainsAny(segment, "*?[") {
			result++
		}
	}

	return result
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
	case StyleVertical:
		result := "Rules (vertical): a boundary may use itself and shared code, never another boundary; shared code may use only shared code; composition may use everything; nothing may use composition."
		if len(this.Layers) > 0 {
			result += fmt.Sprintf(" Inside a boundary, layers run top to bottom as %s, and each may use itself and the layers below it.", strings.Join(this.topDown(), ", "))
		}

		return result
	case StyleModular:
		return "Rules (modular monolith): a boundary may use its own packages, the public packages of other boundaries and shared code; boundaries may not depend on each other in a cycle; shared code may use only shared code; composition may use everything; nothing may use composition."
	case StyleSnowflake:
		return this.starSummary()
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
//     allow, a boundary without a name or globs, a star or an entry that
//     does not fit the star tree, or a setting the style does not use.
func (this Architecture) Validate() error {
	if !slices.Contains(Styles, this.Style) {
		return fmt.Errorf("%w: unknown architecture %q; use one of %s", ErrArchitecture, this.Style, strings.Join(Styles, ", "))
	}

	if this.Style != StyleSnowflake && len(this.Stars) > 0 {
		return fmt.Errorf("%w: stars apply only to the snowflake architecture", ErrArchitecture)
	}

	if this.Style == StyleSnowflake {
		return this.validateStars()
	}

	if this.Style == StyleNone && (len(this.Composition) > 0 || len(this.Layers) > 0 || len(this.Shared) > 0 || len(this.Boundaries) > 0 || len(this.Public) > 0) {
		return fmt.Errorf("%w: the none architecture has no composition, layers, shared code or boundaries; choose another architecture to use them", ErrArchitecture)
	}

	if !this.bounded() && (len(this.Shared) > 0 || len(this.Boundaries) > 0 || len(this.Public) > 0) {
		return fmt.Errorf("%w: shared, boundaries and public apply only to the vertical and modular architectures", ErrArchitecture)
	}

	if this.Style == StyleVertical && len(this.Public) > 0 {
		return fmt.Errorf("%w: public applies only to the modular architecture", ErrArchitecture)
	}

	seen := map[string]bool{}
	for _, layer := range this.Layers {
		if layer.Name == graph.LayerComposition || layer.Name == graph.LayerShared || layer.Name == graph.LayerUnclassified || layer.Name == graph.LayerBoundary || layer.Name == graph.LayerNone {
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
			return fmt.Errorf("%w: the modular architecture has no layers; list boundaries instead", ErrArchitecture)
		}
	}

	if this.bounded() && len(this.Boundaries) == 0 {
		return fmt.Errorf("%w: the %s architecture needs at least one boundary", ErrArchitecture, this.Style)
	}

	names := map[string]bool{}
	for _, boundary := range this.Boundaries {
		switch {
		case boundary.Name == "":
			return fmt.Errorf("%w: every boundary needs a name", ErrArchitecture)
		case names[boundary.Name]:
			return fmt.Errorf("%w: boundary %q is listed twice", ErrArchitecture, boundary.Name)
		case len(boundary.Globs) == 0:
			return fmt.Errorf("%w: boundary %q needs at least one glob", ErrArchitecture, boundary.Name)
		}

		names[boundary.Name] = true
	}

	return nil
}

// Violations finds every module edge that breaks a rule of the
// architecture. For the modular style, it also finds every edge between
// boundaries that closes a cycle, unless the edge already breaks a rule.
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

		for _, cycle := range boundaryCycles(g) {
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
		return false, fmt.Sprintf("shared may not depend on boundary %s: shared code may use only shared code", to.Boundary)
	case from.Boundary == to.Boundary || to.Public:
		return true, ""
	default:
		return false, fmt.Sprintf("boundary %s may not use the internals of boundary %s: other boundaries may use only its public packages; make it public, or change boundaries in treaty.yaml", from.Boundary, to.Boundary)
	}
}

// checkVertical applies the vertical architecture's rules between two
// placed modules, neither of them composition or unclassified.
func (this Architecture) checkVertical(from, to Placement) (bool, string) {
	switch {
	case to.Layer == graph.LayerShared:
		return true, ""
	case from.Layer == graph.LayerShared:
		return false, fmt.Sprintf("shared may not depend on boundary %s: shared code may use only shared code", to.Boundary)
	case from.Boundary != to.Boundary:
		return false, fmt.Sprintf("boundary %s may not depend on boundary %s: boundaries are independent; move the code both use into shared, or change boundaries in treaty.yaml", from.Boundary, to.Boundary)
	case this.rank(to.Layer) > this.rank(from.Layer):
		return false, fmt.Sprintf("in boundary %s, %s may not depend on %s: a layer may use only itself and the layers below it", from.Boundary, from.Layer, to.Layer)
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
	if this.Style == StyleSnowflake {
		return starName(placement)
	}

	if placement.Boundary != "" && placement.Layer != graph.LayerShared {
		return "boundary " + placement.Boundary
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

// bounded reports whether the style divides code into boundaries.
func (this Architecture) bounded() bool {
	return this.Style == StyleVertical || this.Style == StyleModular
}

// within lists a layer and every layer inside or below it, innermost first.
func (this Architecture) within(layer string) []string {
	names := this.LayerNames()
	return names[:min(this.rank(layer)+1, len(names))]
}

// boundaryCycles finds every module edge between two boundaries that depend
// on each other, directly or through other boundaries.
func boundaryCycles(g *graph.Graph) []Violation {
	type crossing struct {
		edge     graph.ModuleEdge
		from, to *graph.Module
	}

	next := map[string][]string{}
	var crossings []crossing
	for _, edge := range g.ModuleEdges() {
		from, to := g.Module(edge.From), g.Module(edge.To)
		if from.Layer != graph.LayerBoundary || to.Layer != graph.LayerBoundary || from.Boundary == to.Boundary {
			continue
		}

		if !slices.Contains(next[from.Boundary], to.Boundary) {
			next[from.Boundary] = append(next[from.Boundary], to.Boundary)
		}

		crossings = append(crossings, crossing{edge, from, to})
	}

	var result []Violation
	for _, each := range crossings {
		back := boundaryPath(next, each.to.Boundary, each.from.Boundary)
		if back == nil {
			continue
		}

		result = append(result, Violation{
			Kind:       FindingCycle,
			Rule:       fmt.Sprintf("boundaries may not depend on each other in a cycle: %s → %s", each.from.Boundary, strings.Join(back, " → ")),
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

// boundaryPath finds the shortest path of boundaries from start to goal,
// including both, or nil when goal is unreachable.
func boundaryPath(next map[string][]string, start, goal string) []string {
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
	return Placement{Layer: module.Layer, Side: module.Side, Boundary: module.Boundary, Public: module.Public, Ambiguous: module.Ambiguous, Star: module.Star, Spoke: module.Spoke, Entry: module.CoreEntry}
}
