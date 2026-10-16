package app

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

// groupSection marks, in a proposal, a module that belongs to a slice or
// context rather than a named group.
const groupSection = "section"

// glob is one proposed config line: a path pattern and its layer group.
type glob struct {
	group   string
	pattern string
}

// Init creates the workspace and proposes a config for one architecture from
// the shape of the import graph, for a person to edit. Choosing the
// architecture is the person's decision; the tool only fits the code to it.
//
// Notes:
//   - For none, the default, the config declares no architecture: nothing is
//     placed and every dependency is allowed.
//   - For hexagonal, clean and layered, programs, such as Go main packages,
//     and other modules nothing imports are composition. A module that
//     imports nothing in the repository but is used goes innermost, or
//     lowest, since a leaf can always sit there. Every other module sits one
//     layer further in than the deepest module importing it. So the
//     proposal has no layer violations by construction.
//   - For vertical and modular, the boundaries are the children of the
//     directory whose children depend on each other least, each named after
//     its directory. For vertical, a child that other children use becomes
//     shared.
//   - For snowflake, see proposeStars: composition, a root star around
//     the rest, each star's core by name or by shape, and the children
//     shaped like stars as branches.
//   - A library module with no imports either way has no shape to place it
//     by. It is left unclassified and listed for a person to place.
//   - Directory names are hints. A hinted layer replaces the shape's answer
//     only when it adds no violation, and it decides an adapter's side.
//
// Parameters:
//   - style: the architecture, one of rules.Styles; empty means none.
//
// Returns:
//   - path: where the draft config was written.
//   - err: the architecture is unknown, or the tree could not be read or
//     the workspace could not be written.
//
// Errors:
//   - rules.ErrArchitecture: style is not one of rules.Styles, or the
//     code has nothing to propose it from, such as vertical for code with
//     no directories to make boundaries of; nothing is written.
func (this *Service) Init(style string) (path string, err error) {
	if style == "" {
		style = rules.StyleNone
	}

	if !slices.Contains(rules.Styles, style) {
		return "", fmt.Errorf("%w: unknown architecture %q; use one of %s", rules.ErrArchitecture, style, strings.Join(rules.Styles, ", "))
	}

	g, err := this.extractor.Extract(this.root)
	if err != nil {
		return "", err
	}

	text, err := proposal(g, style)
	if err != nil {
		return "", err
	}

	if err := this.workspace.Init(); err != nil {
		return "", err
	}

	return this.workspace.WriteConfigDraft(text)
}

// proposal drafts treaty.yaml for an architecture, refusing one Treaty
// could not load, such as vertical or modular for code with no
// directories to make boundaries of.
func proposal(g *graph.Graph, style string) (text string, err error) {
	architecture, text := propose(g, style)
	if err := architecture.Validate(); err != nil {
		if style == rules.StyleVertical || style == rules.StyleModular {
			return "", fmt.Errorf("%w: found no directory with two or more packages to make boundaries of, so %s does not fit this code; choose another architecture, or write boundaries in treaty.yaml yourself", rules.ErrArchitecture, style)
		}

		if style == rules.StyleSnowflake {
			return "", fmt.Errorf("%w: found no packages outside composition to make a star of, so snowflake does not fit this code; choose another architecture, or write stars in treaty.yaml yourself", rules.ErrArchitecture)
		}

		return "", err
	}

	return text, nil
}

// adopt replaces treaty.yaml with a config proposed for an architecture, as
// Init would propose it.
func (this *Service) adopt(style string) (path string, err error) {
	if !slices.Contains(rules.Styles, style) {
		return "", fmt.Errorf("%w: unknown architecture %q; use one of %s", rules.ErrArchitecture, style, strings.Join(rules.Styles, ", "))
	}

	g, err := this.extractor.Extract(this.root)
	if err != nil {
		return "", err
	}

	text, err := proposal(g, style)
	if err != nil {
		return "", err
	}

	return this.workspace.WriteConfig(text)
}

// adapterSide guesses an adapter's side from what it does with the core:
// implementing an inner interface is driven, calling the application is
// driving.
func adapterSide(g *graph.Graph, id string, imports []string, layers map[string]string) string {
	for _, edge := range g.Edges {
		from, to := g.Symbol(edge.From), g.Symbol(edge.To)
		if edge.Kind == graph.EdgeImplements && from.Module == id && to.Module != id && layers[to.Module] != graph.LayerAdapter {
			return graph.SideDriven
		}
	}

	for _, other := range imports {
		if layers[other] == graph.LayerApplication {
			return graph.SideDriving
		}
	}

	return graph.SideDriven
}

// architectureFrom builds the rules a proposal stands for.
func architectureFrom(style, root string, proposal map[string]string) rules.Architecture {
	groups := map[string][]string{}
	for _, glob := range compressGlobs(proposal) {
		groups[glob.group] = append(groups[glob.group], glob.pattern)
	}

	result := rules.Architecture{Style: style, Composition: groups[graph.LayerComposition]}
	switch style {
	case rules.StyleHexagonal:
		result.Layers = []rules.Layer{
			{Name: graph.LayerDomain, Globs: groups[graph.LayerDomain]},
			{Name: graph.LayerApplication, Globs: groups[graph.LayerApplication]},
			{Name: graph.LayerAdapter, Side: graph.SideDriving, Globs: groups[graph.SideDriving]},
			{Name: graph.LayerAdapter, Side: graph.SideDriven, Globs: groups[graph.SideDriven]},
		}
	case rules.StyleVertical, rules.StyleModular:
		result.Shared = groups[graph.LayerShared]
		result.Boundaries = proposedBoundaries(root, proposal)
	default:
		names := outsideIn(style)
		for i := len(names) - 1; i >= 0; i-- {
			result.Layers = append(result.Layers, rules.Layer{Name: names[i], Globs: groups[names[i]]})
		}
	}

	return result
}

// compressGlobs turns a proposal keyed by module path into globs, using
// dir/** for the highest directory whose modules all share one group.
// Modules in a slice or context, and unplaced ones, get no glob.
func compressGlobs(proposal map[string]string) []glob {
	uniform := func(dir string) bool {
		group := ""
		for modulePath, each := range proposal {
			if modulePath == dir || strings.HasPrefix(modulePath, dir+"/") {
				if group != "" && group != each {
					return false
				}

				group = each
			}
		}

		return true
	}

	seen := map[glob]bool{}
	var result []glob
	for modulePath, group := range proposal {
		pattern := modulePath
		if modulePath != "." {
			segments := strings.Split(modulePath, "/")
			for i := 1; i <= len(segments); i++ {
				if dir := strings.Join(segments[:i], "/"); uniform(dir) {
					pattern = dir + "/**"
					break
				}
			}
		}

		if entry := (glob{group, pattern}); group != "" && group != groupSection && !seen[entry] {
			seen[entry] = true
			result = append(result, entry)
		}
	}

	return result
}

// guessLayer maps a module path to the layer its directory names suggest
// for an architecture, or empty when none does. For hexagonal, an adapter
// is named by its side, driving or driven, when its name says which; a name
// such as adapters says only adapter, and the side comes from the code.
func guessLayer(style, modulePath string) string {
	segments := strings.Split(strings.ToLower(modulePath), "/")
	has := func(names ...string) bool {
		for _, segment := range segments {
			if slices.Contains(names, segment) {
				return true
			}
		}

		return false
	}

	prefixed := func(prefixes ...string) bool {
		last := segments[len(segments)-1]
		for _, prefix := range prefixes {
			if strings.HasPrefix(last, prefix) {
				return true
			}
		}

		return false
	}

	if has("cmd", "main") {
		return graph.LayerComposition
	}

	switch style {
	case rules.StyleClean:
		switch {
		case has("domain", "model", "entity", "entities", "core"):
			return "entities"
		case has("usecase", "usecases", "use_cases", "app", "application", "interactors"):
			return "use_cases"
		case has("controllers", "presenters", "gateways", "adapter", "adapters", "interfaces"):
			return "interface_adapters"
		case has("frameworks", "drivers", "infra", "infrastructure", "db", "database", "web", "http", "ui"):
			return "frameworks"
		}
	case rules.StyleLayered:
		switch {
		case has("cli", "http", "api", "web", "grpc", "ui", "handlers", "controllers", "views", "presentation"):
			return "presentation"
		case has("store", "storage", "db", "database", "repository", "repositories", "dao", "persistence", "data"):
			return "data"
		case has("service", "services", "domain", "logic", "core", "app", "application", "business"):
			return "business"
		}
	default:
		// A name that says which side an adapter is on decides it; one that
		// says only "adapter" leaves the side to what the code does.
		switch {
		case has("domain", "model", "entity", "entities", "core"):
			return graph.LayerDomain
		case has("cli", "http", "api", "web", "grpc", "mcp", "server", "handlers") || prefixed("http", "grpc", "rest"):
			return graph.SideDriving
		case has("storage", "db", "database", "repository", "persistence"):
			return graph.SideDriven
		case has("adapter", "adapters", "infra", "infrastructure"):
			return graph.LayerAdapter
		case has("app", "application", "ports", "usecase", "usecases", "service", "services"):
			return graph.LayerApplication
		}
	}

	return ""
}

// layerDraft writes the proposed config for hexagonal, clean or layered.
func layerDraft(style string, proposal map[string]string) string {
	groups := map[string][]string{}
	for _, glob := range compressGlobs(proposal) {
		groups[glob.group] = append(groups[glob.group], glob.pattern)
	}

	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	builder.WriteString("# Modules nothing imports are composition; the rest sit one layer inside the\n")
	builder.WriteString("# deepest module that imports them. Directory names only break ties.\n")
	writeUnplaced(&builder, proposal, "with no imports either way to place them by")
	if style == rules.StyleHexagonal {
		fmt.Fprintf(&builder, "version: %d\narchitecture: hexagonal\n", ConfigVersion)
		builder.WriteString("layers:\n")
		for _, group := range []string{graph.LayerComposition, graph.LayerDomain, graph.LayerApplication} {
			fmt.Fprintf(&builder, "  %s: %s\n", group, yamlList(groups[group]))
		}

		builder.WriteString("  adapter:\n")
		fmt.Fprintf(&builder, "    driving: %s\n", yamlList(groups[graph.SideDriving]))
		fmt.Fprintf(&builder, "    driven: %s\n", yamlList(groups[graph.SideDriven]))
		builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified))
		return builder.String()
	}

	fmt.Fprintf(&builder, "version: %d\narchitecture: %s\n", ConfigVersion, style)
	fmt.Fprintf(&builder, "composition: %s\n", yamlList(groups[graph.LayerComposition]))
	if style == rules.StyleClean {
		builder.WriteString("layers: # outermost first; each may use itself and every ring inside it\n")
	} else {
		builder.WriteString("layers: # top to bottom; each may use itself and every layer below it\n")
	}

	for _, name := range outsideIn(style) {
		fmt.Fprintf(&builder, "  %s: %s\n", name, yamlList(groups[name]))
	}

	builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified))
	return builder.String()
}

// outsideIn lists an architecture's proposed layers, outermost or highest
// first.
func outsideIn(style string) []string {
	switch style {
	case rules.StyleClean:
		result := slices.Clone(rules.CleanLayers)
		slices.Reverse(result)
		return result
	case rules.StyleLayered:
		return []string{"presentation", "business", "data"}
	default:
		return []string{graph.LayerAdapter, graph.LayerApplication, graph.LayerDomain}
	}
}

// propose fits a graph to an architecture from the shape of its imports.
//
// Returns:
//   - architecture: the proposed rules.
//   - text: the same proposal as a treaty.yaml draft.
func propose(g *graph.Graph, style string) (architecture rules.Architecture, text string) {
	switch style {
	case rules.StyleNone:
		return rules.Architecture{Style: rules.StyleNone}, noneDraft()
	case rules.StyleVertical, rules.StyleModular:
		root, proposal := proposeSections(g, style)
		return architectureFrom(style, root, proposal), sectionDraft(style, root, proposal)
	case rules.StyleSnowflake:
		return proposeStars(g)
	default:
		proposal := proposeLayers(g, style)
		return architectureFrom(style, "", proposal), layerDraft(style, proposal)
	}
}

// coreNames are the directory names that make a child of a star its core,
// in the order they are tried after core itself.
var coreNames = []string{"contracts", "ports", "api", "model", "domain"}

// factoryNames are the base names of packages that wire others together,
// which init makes composition when only composition uses them.
var factoryNames = []string{"factory", "wire", "wiring", "compose", "composition", "bootstrap"}

// proposeStars fits a graph to the snowflake architecture.
//
// Notes:
//   - Composition is every entry module, every module nothing in the
//     repository imports that imports something, and then every module
//     that only composition imports and that is the repository root, the
//     front door, or is named like a factory, such as factory or wire.
//   - The root star is the deepest directory holding every other module
//     that imports or is imported. A module with no imports either way has
//     no shape to place it by; it is left out here and placed by the stars
//     that result.
//   - A star's core is its child named core; failing that, its children
//     named contracts, ports, api, model or domain; failing that, each
//     child that a sibling uses and that uses no sibling; and failing
//     that, only the star's own package.
//   - A child that is not core is a branch, a star of its own, when it has
//     two or more child directories holding modules and one of them would
//     be its core by name or by being a sink its siblings use.
//   - The draft is not free of violations by construction: code written
//     with sideways dependencies cannot be fitted without moving it. Its
//     header counts what treaty check will list.
//
// Returns:
//   - architecture: the proposed rules.
//   - text: the same proposal as a treaty.yaml draft.
func proposeStars(g *graph.Graph) (architecture rules.Architecture, text string) {
	importers, imports := map[string][]string{}, map[string][]string{}
	for _, edge := range g.ModuleEdges() {
		importers[edge.To] = append(importers[edge.To], edge.From)
		imports[edge.From] = append(imports[edge.From], edge.To)
	}

	composition := map[string]bool{}
	for _, module := range g.Modules {
		if module.Entry || (len(importers[module.ID]) == 0 && len(imports[module.ID]) > 0) {
			composition[module.ID] = true
		}
	}

	for changed := true; changed; {
		changed = false
		for _, module := range g.Modules {
			named := module.Path == "." || slices.Contains(factoryNames, path.Base(module.Path))
			only := len(importers[module.ID]) > 0 && !slices.ContainsFunc(importers[module.ID], func(id string) bool { return !composition[id] })
			if !composition[module.ID] && named && only {
				composition[module.ID], changed = true, true
			}
		}
	}

	proposal := map[string]string{}
	var shaped []string
	members := map[string]bool{}
	for _, module := range g.Modules {
		proposal[module.Path] = groupSection
		switch {
		case composition[module.ID]:
			proposal[module.Path] = graph.LayerComposition
		case len(importers[module.ID]) > 0 || len(imports[module.ID]) > 0:
			shaped = append(shaped, module.Path)
			members[module.Path] = true
		default:
			members[module.Path] = true
		}
	}

	architecture = rules.Architecture{Style: rules.StyleSnowflake}
	for _, glob := range compressGlobs(proposal) {
		architecture.Composition = append(architecture.Composition, glob.pattern)
	}

	sort.Strings(architecture.Composition)
	fit := starFit{graph: g, members: members}
	if len(shaped) > 0 {
		fit.star(commonDir(shaped))
	}

	architecture.Stars = fit.stars
	return architecture, starDraft(g, architecture, fit)
}

// starFit gathers the stars proposeStars finds, root first, each star
// before the stars inside it.
type starFit struct {
	graph   *graph.Graph
	members map[string]bool
	stars   []rules.Star

	// shared lists, for each star, the children two or more of its other
	// children use: what its core could be made of.
	shared map[string][]string
}

// star proposes a star at a directory, its core, and its branches.
func (this *starFit) star(dir string) {
	children, uses, usedBy := this.children(dir)
	var core []string
	switch {
	case children["core"]:
		core = []string{"core"}
	default:
		for _, name := range coreNames {
			if children[name] {
				core = append(core, name)
			}
		}

		if len(core) == 0 {
			core = sinks(children, uses, usedBy)
		}
	}

	star := rules.Star{Dir: dir, Core: []string{}}
	for _, name := range core {
		star.Core = append(star.Core, strings.TrimPrefix(path.Join(dir, name)+"/**", "./"))
	}

	if len(core) == 0 {
		if this.shared == nil {
			this.shared = map[string][]string{}
		}

		for _, name := range sortedKeys(children) {
			if len(usedBy[name]) >= 2 {
				this.shared[dir] = append(this.shared[dir], path.Join(dir, name))
			}
		}
	}

	this.stars = append(this.stars, star)
	for _, name := range sortedKeys(children) {
		if !slices.Contains(core, name) && this.branches(path.Join(dir, name)) {
			this.star(path.Join(dir, name))
		}
	}
}

// branches reports whether a child of a star is a star of its own: it has
// two or more child directories holding modules, and one of them would be
// its core.
func (this *starFit) branches(dir string) bool {
	children, uses, usedBy := this.children(dir)
	if len(children) < 2 {
		return false
	}

	for name := range children {
		if name == "core" || slices.Contains(coreNames, name) {
			return true
		}
	}

	return len(sinks(children, uses, usedBy)) > 0
}

// children finds the child directories of dir that hold modules outside
// composition, and which of them use which others.
func (this *starFit) children(dir string) (children map[string]bool, uses, usedBy map[string]map[string]bool) {
	children, uses, usedBy = map[string]bool{}, map[string]map[string]bool{}, map[string]map[string]bool{}
	of := map[string]string{}
	for _, module := range this.graph.Modules {
		if child, ok := childOf(dir, module.Path); ok && this.members[module.Path] {
			children[child], of[module.ID] = true, child
		}
	}

	for _, edge := range this.graph.ModuleEdges() {
		from, fromInside := of[edge.From]
		to, toInside := of[edge.To]
		if !fromInside || !toInside || from == to {
			continue
		}

		if uses[from] == nil {
			uses[from] = map[string]bool{}
		}

		if usedBy[to] == nil {
			usedBy[to] = map[string]bool{}
		}

		uses[from][to], usedBy[to][from] = true, true
	}

	return children, uses, usedBy
}

// sinks lists the children a sibling uses that use no sibling, by name.
func sinks(children map[string]bool, uses, usedBy map[string]map[string]bool) (result []string) {
	for _, name := range sortedKeys(children) {
		if len(usedBy[name]) > 0 && len(uses[name]) == 0 {
			result = append(result, name)
		}
	}

	return result
}

// sortedKeys lists a set's members in order.
func sortedKeys(set map[string]bool) []string {
	result := make([]string, 0, len(set))
	for key := range set {
		result = append(result, key)
	}

	sort.Strings(result)
	return result
}

// starDraft writes the proposed config for snowflake. Its header lists the
// star tree, counts the dependencies treaty check will list against it by
// kind, says which stars have no core yet, and lists the modules outside
// the root star.
func starDraft(g *graph.Graph, architecture rules.Architecture, fit starFit) string {
	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	builder.WriteString("# A snowflake is a star of stars: each star's core holds its contracts and\n")
	builder.WriteString("# orchestrators, and every other child directory is a spoke. A spoke may use\n")
	builder.WriteString("# its own star's core and the cores of the stars around it, never a sibling;\n")
	builder.WriteString("# composition wires spokes together. Run treaty check to see the sideways\n")
	builder.WriteString("# dependencies left, and move each behind a port in a core.\n")
	if len(architecture.Stars) > 0 {
		writeStarTree(&builder, g, architecture, fit)
	}

	fmt.Fprintf(&builder, "version: %d\narchitecture: snowflake\n", ConfigVersion)
	fmt.Fprintf(&builder, "composition: %s\n", yamlList(architecture.Composition))
	builder.WriteString("stars: # each star's directory -> the globs of its core; a star's own package is in its core\n")
	for _, star := range architecture.Stars {
		fmt.Fprintf(&builder, "  %s: %s\n", yamlKey(star.Dir), yamlList(star.Core))
	}

	builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified))
	return builder.String()
}

// writeStarTree writes the draft's comments on its stars: one line per
// star, the dependencies that break the rules by kind, the stars without a
// core, and the modules outside the root star.
func writeStarTree(builder *strings.Builder, g *graph.Graph, architecture rules.Architecture, fit starFit) {
	paths := make([]string, 0, len(g.Modules))
	for _, module := range g.Modules {
		paths = append(paths, module.Path)
	}

	builder.WriteString("#\n")
	for _, star := range architecture.Tree(paths) {
		line := fmt.Sprintf("#   %s  core %s", star.Dir, strings.Join(append([]string{star.Dir}, star.Core...), ", "))
		if star.Parent != "" {
			line = fmt.Sprintf("#   %s  branch of %s; core %s", star.Dir, star.Parent, strings.Join(append([]string{star.Dir}, star.Core...), ", "))
		}

		if len(star.Leaves) > 0 {
			line += "; leaves " + strings.Join(star.Leaves, ", ")
		}

		if len(star.Branches) > 0 {
			line += "; branches " + strings.Join(star.Branches, ", ")
		}

		builder.WriteString(line + "\n")
	}

	placed := g.Clone()
	architecture.Assign(placed)
	counts := map[string]int{}
	violations := architecture.Violations(placed)
	for _, violation := range violations {
		from, to := placement(placed.Module(violation.From)), placement(placed.Module(violation.To))
		switch {
		case to.Layer == graph.LayerComposition:
			counts["on composition"]++
		case from.Layer == graph.LayerCore && rules.Encloses(from.Star, to.Star):
			counts["from a core into its own spokes"]++
		default:
			counts["between sibling spokes"]++
		}
	}

	switch len(violations) {
	case 0:
		builder.WriteString("# treaty check lists no dependency that breaks these rules.\n")
	default:
		var parts []string
		for _, kind := range []string{"between sibling spokes", "from a core into its own spokes", "on composition"} {
			if counts[kind] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
			}
		}

		noun := "dependencies that break"
		if len(violations) == 1 {
			noun = "dependency that breaks"
		}

		fmt.Fprintf(builder, "# treaty check lists %d %s these rules: %s.\n", len(violations), noun, strings.Join(parts, ", "))
	}

	for _, star := range architecture.Stars {
		if len(star.Core) > 0 {
			continue
		}

		core := strings.TrimPrefix(path.Join(star.Dir, "core"), "./")
		fmt.Fprintf(builder, "# No core in %s yet: every dependency between its children is sideways. Create %s and move the contracts there.\n", star.Dir, core)
		if candidates := fit.shared[star.Dir]; len(candidates) > 0 {
			fmt.Fprintf(builder, "# Candidates for %s: %s.\n", core, strings.Join(candidates, ", "))
		}
	}

	unplaced := map[string]string{}
	for _, module := range placed.Modules {
		if module.Layer == graph.LayerUnclassified {
			unplaced[module.Path] = ""
		}
	}

	writeUnplaced(builder, unplaced, "outside the root star and not composition")
}

// commonDir is the deepest directory holding every one of the paths, or
// the repository root.
func commonDir(paths []string) string {
	result := strings.Split(paths[0], "/")
	for _, each := range paths[1:] {
		segments := strings.Split(each, "/")
		n := 0
		for n < len(result) && n < len(segments) && result[n] == segments[n] {
			n++
		}

		result = result[:n]
	}

	if len(result) == 0 {
		return "."
	}

	return strings.Join(result, "/")
}

// proposeLayers places every module of an unlayered graph by the shape of
// its imports, then applies directory-name hints that keep it valid.
//
// Returns:
//   - result: the group keyed by module path: composition or a layer name,
//     with hexagonal adapters named by side; empty for a module left
//     unclassified.
func proposeLayers(g *graph.Graph, style string) map[string]string {
	rings := outsideIn(style)
	deepest := len(rings)
	architecture := rules.Architecture{Style: style}
	for i := len(rings) - 1; i >= 0; i-- {
		architecture.Layers = append(architecture.Layers, rules.Layer{Name: rings[i]})
	}

	importers, imports := map[string][]string{}, map[string][]string{}
	for _, edge := range g.ModuleEdges() {
		importers[edge.To] = append(importers[edge.To], edge.From)
		imports[edge.From] = append(imports[edge.From], edge.To)
	}

	// Longest path from the entry points, capped at the innermost layer so
	// that cycles settle.
	level := map[string]int{}
	for range g.Modules {
		changed := false
		for _, module := range g.Modules {
			for _, importer := range importers[module.ID] {
				if next := min(level[importer]+1, deepest); next > level[module.ID] {
					level[module.ID], changed = next, true
				}
			}
		}

		if !changed {
			break
		}
	}

	byLevel := append([]string{graph.LayerComposition}, rings...)
	layers := map[string]string{}
	for _, module := range g.Modules {
		switch {
		case module.Entry:
			layers[module.ID] = graph.LayerComposition
		case len(importers[module.ID]) == 0 && len(imports[module.ID]) == 0:
			layers[module.ID] = graph.LayerUnclassified
		case len(imports[module.ID]) == 0:
			layers[module.ID] = rings[deepest-1]
		default:
			layers[module.ID] = byLevel[level[module.ID]]
		}
	}

	allowed := func(from, to string) bool {
		ok, _ := architecture.Check(rules.Placement{Layer: from}, rules.Placement{Layer: to})
		return ok
	}

	valid := func(id, layer string) bool {
		for _, other := range imports[id] {
			if !allowed(layer, layers[other]) {
				return false
			}
		}

		for _, other := range importers[id] {
			if !allowed(layers[other], layer) {
				return false
			}
		}

		return true
	}

	sides := map[string]string{}
	for _, module := range g.Modules {
		hint := guessLayer(style, module.Path)
		layer := hint
		if hint == graph.SideDriving || hint == graph.SideDriven {
			layer, sides[module.ID] = graph.LayerAdapter, hint
		}

		if hint != "" && layer != layers[module.ID] && valid(module.ID, layer) {
			layers[module.ID] = layer
		}
	}

	result := map[string]string{}
	for _, module := range g.Modules {
		group := layers[module.ID]
		if group == graph.LayerUnclassified {
			group = ""
		}

		if group == graph.LayerAdapter {
			group = sides[module.ID]
			if group == "" {
				group = adapterSide(g, module.ID, imports[module.ID], layers)
			}
		}

		result[module.Path] = group
	}

	return result
}

// proposeSections finds the directory whose children make the best
// boundaries, and places every module around them.
//
// Notes:
//   - A candidate is any directory with two or more children holding
//     modules that are not programs. Its score is the number of children
//     that can stand alone: for vertical, a child that another child uses
//     becomes shared instead; for modular, a child whose internal packages
//     another child uses counts against it. The highest score wins, then a
//     name such as features or modules, then the shallower directory.
//   - Outside the chosen directory, programs and modules that use a
//     boundary are composition, and modules only used by them are
//     shared.
//
// Returns:
//   - root: the chosen directory, empty when no directory has two children.
//   - result: composition, shared or section keyed by module path; empty
//     for a module left unclassified.
func proposeSections(g *graph.Graph, style string) (root string, result map[string]string) {
	importers, imports := map[string][]string{}, map[string][]string{}
	for _, edge := range g.ModuleEdges() {
		importers[edge.To] = append(importers[edge.To], edge.From)
		imports[edge.From] = append(imports[edge.From], edge.To)
	}

	type candidate struct {
		dir      string
		shared   map[string]bool
		score    int
		named    bool
		depth    int
		children map[string]string
	}

	var best *candidate
	for _, dir := range sectionDirs(g) {
		children := map[string]string{}
		count := map[string]bool{}
		for _, module := range g.Modules {
			if child, ok := childOf(dir, module.Path); ok {
				children[module.ID] = child
				if !module.Entry {
					count[child] = true
				}
			}
		}

		if len(count) < 2 {
			continue
		}

		shared := map[string]bool{}
		penalty := map[string]bool{}
		for id, child := range children {
			for _, importer := range importers[id] {
				other, inside := children[importer]
				if !inside || other == child {
					continue
				}

				shared[child] = true
				if path.Join(dir, child) != g.Module(id).Path {
					penalty[child] = true
				}
			}
		}

		if style == rules.StyleModular {
			shared = map[string]bool{}
		} else {
			// Shared code may use only shared code, so whatever a shared child
			// uses is shared too.
			for changed := true; changed; {
				changed = false
				for id, child := range children {
					if !shared[child] {
						continue
					}

					for _, other := range imports[id] {
						if next, inside := children[other]; inside && !shared[next] {
							shared[next], changed = true, true
						}
					}
				}
			}

			penalty = shared
		}

		score := 0
		for child := range count {
			if !penalty[child] {
				score++
			}
		}

		name := strings.ToLower(path.Base(dir))
		current := &candidate{
			dir: dir, shared: shared, score: score, children: children,
			named: slices.Contains([]string{"features", "slices", "modules", "contexts", "domains", "components"}, name),
			depth: strings.Count(dir, "/"),
		}

		switch {
		case score < 2:
		case best == nil,
			score > best.score,
			score == best.score && current.named && !best.named,
			score == best.score && current.named == best.named && current.depth < best.depth:
			best = current
		}
	}

	result = map[string]string{}
	if best == nil {
		for _, module := range g.Modules {
			if module.Entry {
				result[module.Path] = graph.LayerComposition
			} else {
				result[module.Path] = ""
			}
		}

		return "", result
	}

	inSection := func(id string) bool {
		child, inside := best.children[id]
		return inside && !best.shared[child]
	}

	for _, module := range g.Modules {
		if child, inside := best.children[module.ID]; inside && !module.Entry {
			result[module.Path] = groupSection
			if best.shared[child] {
				result[module.Path] = graph.LayerShared
			}

			continue
		}

		usesSection, usedBySection := false, false
		for _, other := range imports[module.ID] {
			usesSection = usesSection || inSection(other)
		}

		for _, other := range importers[module.ID] {
			usedBySection = usedBySection || inSection(other)
		}

		switch {
		case module.Entry || (usesSection && !usedBySection):
			result[module.Path] = graph.LayerComposition
		case usedBySection && !usesSection:
			result[module.Path] = graph.LayerShared
		default:
			result[module.Path] = ""
		}
	}

	return best.dir, result
}

// childOf finds the child of dir that holds a module path, if any.
func childOf(dir, modulePath string) (child string, ok bool) {
	prefix := dir + "/"
	if dir == "." {
		prefix = ""
	}

	if !strings.HasPrefix(modulePath, prefix) || modulePath == dir || modulePath == "." {
		return "", false
	}

	child, _, _ = strings.Cut(strings.TrimPrefix(modulePath, prefix), "/")
	return child, true
}

// sectionDirs lists every directory above a module, including the root.
func sectionDirs(g *graph.Graph) []string {
	seen := map[string]bool{".": true}
	for _, module := range g.Modules {
		segments := strings.Split(module.Path, "/")
		for i := 1; i < len(segments); i++ {
			seen[strings.Join(segments[:i], "/")] = true
		}
	}

	result := make([]string, 0, len(seen))
	for dir := range seen {
		result = append(result, dir)
	}

	sort.Strings(result)
	return result
}

// noneDraft writes the config that declares no architecture.
func noneDraft() string {
	var builder strings.Builder
	builder.WriteString("# Written by treaty init. No architecture is declared, so every dependency is\n")
	builder.WriteString("# allowed; contracts and their changes are still checked. To declare one, run\n")
	builder.WriteString("# treaty init --architecture <hexagonal, clean, layered, vertical, modular or\n# snowflake>.\n")
	fmt.Fprintf(&builder, "version: %d\narchitecture: none\n", ConfigVersion)
	builder.WriteString(rulesDraft(rules.FindingBreaking))
	return builder.String()
}

// sectionDraft writes the proposed config for vertical or modular.
func sectionDraft(style, root string, proposal map[string]string) string {
	groups := map[string][]string{}
	for _, glob := range compressGlobs(proposal) {
		groups[glob.group] = append(groups[glob.group], glob.pattern)
	}

	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	builder.WriteString("# Each child of the directory whose children depend on each other least is a\n# boundary.")
	if style == rules.StyleVertical {
		builder.WriteString(" Children that other boundaries use are shared. Run treaty check to\n# see what is left.\n")
	} else {
		builder.WriteString(" Other boundaries may use only a boundary's public packages. Run\n# treaty check to see which internals are used across boundaries.\n")
	}

	writeUnplaced(&builder, proposal, "neither only using nor only used by the boundaries")
	fmt.Fprintf(&builder, "version: %d\narchitecture: %s\n", ConfigVersion, style)
	fmt.Fprintf(&builder, "composition: %s\n", yamlList(groups[graph.LayerComposition]))
	fmt.Fprintf(&builder, "shared: %s\n", yamlList(groups[graph.LayerShared]))
	boundaries := proposedBoundaries(root, proposal)
	if len(boundaries) == 0 {
		builder.WriteString("# No directory has two independent children; name the boundaries by hand,\n# such as orders: [\"web/orders/**\", \"store/orders/**\"].\n")
		builder.WriteString("boundaries: {}\n")
	} else {
		builder.WriteString("boundaries: # each name gathers the modules its globs match\n")
		for _, boundary := range boundaries {
			fmt.Fprintf(&builder, "  %s: %s\n", yamlKey(boundary.Name), yamlList(boundary.Globs))
		}
	}

	if style == rules.StyleVertical {
		builder.WriteString("# Optional layers inside every boundary, top to bottom; each may use itself and\n# the layers below it:\n")
		builder.WriteString("# layers:\n#   api: [\"**/api/**\"]\n#   store: [\"**/store/**\"]\n")
		builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified, rules.FindingAmbiguous))
	} else {
		builder.WriteString("# By default other boundaries may use the top package of each boundary glob;\n# list globs to choose the public packages instead:\n# public: [\"**/api/**\"]\n")
		builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingCycle, rules.FindingUnclassified, rules.FindingAmbiguous))
	}

	return builder.String()
}

// proposedBoundaries makes a boundary of each child of root that holds a
// module the proposal places in a section, named after the child.
func proposedBoundaries(root string, proposal map[string]string) (result []rules.Boundary) {
	seen := map[string]bool{}
	for modulePath, group := range proposal {
		if child, ok := childOf(root, modulePath); ok && group == groupSection {
			seen[child] = true
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}

	sort.Strings(names)
	for _, name := range names {
		result = append(result, rules.Boundary{Name: name, Globs: []string{strings.TrimPrefix(path.Join(root, name)+"/**", "./")}})
	}

	return result
}

// rulesDraft writes the rules section that fails on the given kinds of
// finding, by their names in treaty.yaml.
func rulesDraft(kinds ...string) string {
	var names []string
	for _, rule := range RuleNames {
		if slices.Contains(kinds, rule.Kind) {
			names = append(names, rule.Name)
		}
	}

	return "rules:\n  fail_on: [" + strings.Join(names, ", ") + "]\n"
}

// plainKey matches a map key YAML reads back unquoted as the same string,
// unless it is a word YAML treats as a boolean or null.
var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./-]*$`)

// yamlKey quotes a map key that YAML would not read back as the same
// string.
func yamlKey(key string) string {
	if plainKey.MatchString(key) && !slices.Contains([]string{"true", "false", "yes", "no", "on", "off", "null"}, strings.ToLower(key)) {
		return key
	}

	return fmt.Sprintf("%q", key)
}

func writeUnplaced(builder *strings.Builder, proposal map[string]string, reason string) {
	var unplaced []string
	for modulePath, group := range proposal {
		if group == "" {
			unplaced = append(unplaced, modulePath)
		}
	}

	sort.Strings(unplaced)
	if len(unplaced) > 0 {
		fmt.Fprintf(builder, "# Unclassified, %s: %s\n", reason, strings.Join(unplaced, ", "))
	}
}

func yamlList(values []string) string {
	sort.Strings(values)
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}

	return "[" + strings.Join(quoted, ", ") + "]"
}
