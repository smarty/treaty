package app

import (
	"fmt"
	"maps"
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
//   - For snowflake, see proposeStars: the repository as the root star,
//     each star's contracts by name or by shape, its orchestrators by what
//     they wire, and the directories shaped like stars as branches.
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

		if style == rules.StyleSnowflake && len(g.Modules) == 0 {
			return "", fmt.Errorf("%w: found no packages to make a star of, so snowflake does not fit this code; choose another architecture, or write stars in treaty.yaml yourself", rules.ErrArchitecture)
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

// contractsNames are the base names that make a position of a star its
// contracts, core last: existing code is fitted, and the draft flags the
// name, since core is the map's region of a star, not a package.
var contractsNames = []string{"contracts", "ports", "api", "model", "domain", "types", "interfaces", "core"}

// orchestratorNames are the base names of packages that wire others, which
// init makes orchestrators when they use or are used by a spoke.
var orchestratorNames = []string{"assembly", "bootstrap", "compose", "composition", "executor", "factory", "mediator", "orchestrator", "pipeline", "wire", "wiring"}

// proposeStars fits a graph to the snowflake architecture.
//
// Notes:
//   - The root star is the repository, ".", which fits every tree. Each
//     star's positions are its packages grouped as placement groups them,
//     looking through directories that hold no package.
//   - A star's contracts are its shallowest positions named like
//     contracts, such as contracts or ports; failing those, the positions
//     two or more others use and that use none.
//   - A directory below a star is a branch, a star of its own, when only
//     directories holding no package lie between them, it neither is nor
//     holds the star's contracts, it groups two or more positions, and it
//     has contracts of its own by the same test. So a container such as
//     internal/, which holds the star's contracts, is looked through.
//   - An orchestrator wires: the star's own package when it uses a spoke,
//     a position that uses two or more spokes, a main package that uses
//     one, and a position named like an orchestrator, such as assembly or
//     executor, that a spoke uses or that uses one. The own package, if it
//     is not an orchestrator, is contracts, since it must be listed. Every
//     other position is a leaf.
//   - The draft is not free of violations by construction: code written
//     with sideways dependencies cannot be fitted without moving it. Its
//     header counts what treaty check will list.
//
// Returns:
//   - architecture: the proposed rules; no stars when the graph has no
//     module.
//   - text: the same proposal as a treaty.yaml draft.
func proposeStars(g *graph.Graph) (architecture rules.Architecture, text string) {
	architecture = rules.Architecture{Style: rules.StyleSnowflake}
	if len(g.Modules) == 0 {
		return architecture, ""
	}

	fit := &starFit{graph: g, holds: map[string]*graph.Module{}, candidates: map[string][]string{}}
	for _, module := range g.Modules {
		fit.holds[module.Path] = module
	}

	fit.star(".")
	architecture.Stars = fit.stars
	return architecture, starDraft(g, architecture, fit)
}

// starFit gathers the stars proposeStars finds, root first, each star
// before the stars inside it.
type starFit struct {
	graph *graph.Graph
	holds map[string]*graph.Module
	stars []rules.Star

	// candidates lists, for each star without contracts, the positions two
	// or more others use: what its contracts could be made of.
	candidates map[string][]string
}

// starShape is a star's positions, which use which, and what each holds.
// The star's own package uses positions under the star's directory, but is
// no position itself.
type starShape struct {
	positions    []string
	modules      map[string][]*graph.Module
	uses, usedBy map[string]map[string]bool
}

// star proposes a star at a directory, its contracts, its orchestrators and
// its branches, and then each branch in turn.
func (this *starFit) star(dir string) {
	shape := this.shape(dir, nil)
	contracts := this.contracts(dir, shape, nil)
	branches := map[string]bool{}
	for _, candidate := range this.branchCandidates(dir) {
		inside := slices.ContainsFunc(slices.Collect(maps.Keys(branches)), func(branch string) bool { return rules.Encloses(branch, candidate) })
		holdsContracts := slices.ContainsFunc(contracts, func(entry string) bool { return rules.Encloses(candidate, entry) })
		if inside || holdsContracts {
			continue
		}

		if own := this.shape(candidate, nil); len(own.positions) >= 2 && len(this.contracts(candidate, own, nil)) > 0 {
			branches[candidate] = true
		}
	}

	shape = this.shape(dir, branches)
	contracts = this.contracts(dir, shape, branches)
	spoke := func(position string) bool { return position != dir && !slices.Contains(contracts, position) }
	spokesOf := func(set map[string]bool) (count int) {
		for position := range set {
			if spoke(position) {
				count++
			}
		}

		return count
	}

	star := rules.Star{Dir: dir, Contracts: []string{}, Orchestrators: []string{}}
	if this.holds[dir] != nil && spokesOf(shape.uses[dir]) > 0 {
		star.Orchestrators = append(star.Orchestrators, dir)
	} else if this.holds[dir] != nil {
		star.Contracts = append(star.Contracts, dir)
	}

	star.Contracts = append(star.Contracts, contracts...)
	for _, position := range shape.positions {
		if branches[position] || !spoke(position) {
			continue
		}

		uses := spokesOf(shape.uses[position])
		main := slices.ContainsFunc(shape.modules[position], func(module *graph.Module) bool { return module.Entry })
		named := slices.Contains(orchestratorNames, path.Base(position)) && (uses > 0 || spokesOf(shape.usedBy[position]) > 0)
		if uses >= 2 || (main && uses > 0) || named {
			star.Orchestrators = append(star.Orchestrators, position)
		}
	}

	if len(contracts) == 0 {
		for _, position := range shape.positions {
			if !branches[position] && len(shape.usedBy[position]) >= 2 {
				this.candidates[dir] = append(this.candidates[dir], position)
			}
		}
	}

	this.stars = append(this.stars, star)
	for _, branch := range slices.Sorted(maps.Keys(branches)) {
		this.star(branch)
	}
}

// shape groups the modules below a directory by their position in it,
// looking through directories that hold no module, each opaque directory
// counting as one position, and records which positions use which.
func (this *starFit) shape(dir string, opaque map[string]bool) (result starShape) {
	result = starShape{modules: map[string][]*graph.Module{}, uses: map[string]map[string]bool{}, usedBy: map[string]map[string]bool{}}
	of := map[string]string{}
	for _, module := range this.graph.Modules {
		switch {
		case module.Path == dir:
			of[module.ID] = dir
		case rules.Encloses(dir, module.Path):
			position := module.Path
			for _, at := range below(dir, module.Path) {
				if this.holds[at] != nil || opaque[at] {
					position = at
					break
				}
			}

			if result.modules[position] == nil {
				result.positions = append(result.positions, position)
			}

			of[module.ID] = position
			result.modules[position] = append(result.modules[position], module)
		}
	}

	sort.Strings(result.positions)
	for _, edge := range this.graph.ModuleEdges() {
		from, fromInside := of[edge.From]
		to, toInside := of[edge.To]
		if !fromInside || !toInside || from == to || to == dir {
			continue
		}

		if result.uses[from] == nil {
			result.uses[from] = map[string]bool{}
		}

		if result.usedBy[to] == nil {
			result.usedBy[to] = map[string]bool{}
		}

		result.uses[from][to], result.usedBy[to][from] = true, true
	}

	return result
}

// contracts finds a star's contracts among its positions other than its
// branches: the shallowest named like contracts, or else the sinks, the
// positions two or more others use and that use none.
func (this *starFit) contracts(dir string, shape starShape, branches map[string]bool) (result []string) {
	shallowest := -1
	for _, position := range shape.positions {
		if branches[position] || !slices.Contains(contractsNames, path.Base(position)) {
			continue
		}

		switch depth := len(below(dir, position)); {
		case shallowest < 0 || depth < shallowest:
			result, shallowest = []string{position}, depth
		case depth == shallowest:
			result = append(result, position)
		}
	}

	if len(result) > 0 {
		return result
	}

	for _, position := range shape.positions {
		if !branches[position] && len(shape.usedBy[position]) >= 2 && len(shape.uses[position]) == 0 {
			result = append(result, position)
		}
	}

	return result
}

// branchCandidates lists the directories below a star that only
// directories holding no module separate from it, shallowest first.
func (this *starFit) branchCandidates(dir string) []string {
	seen := map[string]bool{}
	for _, module := range this.graph.Modules {
		if module.Path == dir || !rules.Encloses(dir, module.Path) {
			continue
		}

		for _, at := range below(dir, module.Path) {
			seen[at] = true
			if this.holds[at] != nil {
				break
			}
		}
	}

	result := slices.Collect(maps.Keys(seen))
	sort.Slice(result, func(i, j int) bool {
		if a, b := strings.Count(result[i], "/"), strings.Count(result[j], "/"); a != b {
			return a < b
		}

		return result[i] < result[j]
	})

	return result
}

// below lists the directories on the way down from a directory, exclusive,
// to a path inside it, inclusive.
func below(dir, inner string) (result []string) {
	at := dir
	for _, segment := range strings.Split(strings.TrimPrefix(inner, strings.TrimPrefix(dir+"/", "./")), "/") {
		at = path.Join(at, segment)
		result = append(result, at)
	}

	return result
}

// starDraft writes the proposed config for snowflake. Its header lists the
// star tree, counts the dependencies treaty check will list against it by
// kind, and says which stars lack contracts or orchestrators.
func starDraft(g *graph.Graph, architecture rules.Architecture, fit *starFit) string {
	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	builder.WriteString("# A snowflake is a star of stars. Each star's core holds its contracts and its\n")
	builder.WriteString("# orchestrators; every other child of a star that holds a package is a spoke, a\n")
	builder.WriteString("# leaf or a branch, a star of its own, and directories that hold no package are\n")
	builder.WriteString("# looked through. A spoke may use only its own star's core. Contracts may use\n")
	builder.WriteString("# their star's contracts and the contracts one star out or in, never a spoke or\n")
	builder.WriteString("# an orchestrator; an orchestrator may use its star's core and leaves, its\n")
	builder.WriteString("# branches' cores and the contracts around it. Run treaty check to see the\n")
	builder.WriteString("# dependencies left that break these rules, and move each behind a port.\n")
	writeStarTree(&builder, g, architecture, fit)
	fmt.Fprintf(&builder, "version: %d\narchitecture: snowflake\nstars:\n", ConfigVersion)
	for _, star := range architecture.Stars {
		fmt.Fprintf(&builder, "  %s:\n    contracts: %s\n    orchestrators: %s\n", yamlKey(star.Dir), entryList(star.Contracts), entryList(star.Orchestrators))
	}

	builder.WriteString(rulesDraft(rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingUnclassified))
	return builder.String()
}

// breakNames words each kind of snowflake break for the draft's count, in
// the order it lists them.
var breakNames = []struct{ kind, words string }{
	{rules.BreakSideways, "between sibling spokes"},
	{rules.BreakOut, "past the core of their own star"},
	{rules.BreakIn, "into a branch beyond its core"},
	{rules.BreakContracts, "from contracts to a spoke or an orchestrator"},
}

// writeStarTree writes the draft's comments on its stars: one line per
// star, the dependencies that break the rules by kind, the contracts named
// core, and the stars without contracts or orchestrators.
func writeStarTree(builder *strings.Builder, g *graph.Graph, architecture rules.Architecture, fit *starFit) {
	bound, _ := architecture.Bind(modulePaths(g))
	builder.WriteString("#\n")
	tree := bound.Tree()
	var named []string
	for _, star := range tree {
		var parts []string
		if star.Parent != "" {
			parts = append(parts, "branch of "+star.Parent)
		}

		parts = append(parts, "contracts "+listOrNone(star.Contracts), "orchestrators "+listOrNone(star.Orchestrators))
		if len(star.Leaves) > 0 {
			parts = append(parts, "leaves "+strings.Join(star.Leaves, ", "))
		}

		if len(star.Branches) > 0 {
			parts = append(parts, "branches "+strings.Join(star.Branches, ", "))
		}

		fmt.Fprintf(builder, "#   %s  %s\n", star.Dir, strings.Join(parts, "; "))
		for _, entry := range star.Contracts {
			if path.Base(entry) == "core" {
				named = append(named, entry)
			}
		}
	}

	placed := g.Clone()
	bound.Assign(placed)
	counts := map[string]int{}
	violations := bound.Violations(placed)
	for _, violation := range violations {
		counts[bound.Break(placement(placed.Module(violation.From)), placement(placed.Module(violation.To)))]++
	}

	switch len(violations) {
	case 0:
		builder.WriteString("# treaty check lists no dependency that breaks these rules.\n")
	default:
		var parts []string
		for _, each := range breakNames {
			if counts[each.kind] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[each.kind], each.words))
			}
		}

		noun := "dependencies that break"
		if len(violations) == 1 {
			noun = "dependency that breaks"
		}

		fmt.Fprintf(builder, "# treaty check lists %d %s these rules: %s.\n", len(violations), noun, strings.Join(parts, ", "))
	}

	if len(named) > 0 {
		verb, own := "are", "their"
		if len(named) == 1 {
			verb, own = "is", "its"
		}

		fmt.Fprintf(builder, "# %s %s contracts by %s name. Core is the map's region of a star, not a package: name each for what it holds, such as %s, and move any orchestrator in it to a package of its own.\n",
			joinAnd(named), verb, own, path.Join(path.Dir(named[0]), "contracts"))
	}

	for _, star := range tree {
		if len(star.Contracts) > 0 {
			continue
		}

		contracts := path.Join(star.Dir, "contracts")
		fmt.Fprintf(builder, "# No contracts in %s yet: its spokes may use only its orchestrators. Create %s and move what they share there.\n", star.Dir, contracts)
		if candidates := fit.candidates[star.Dir]; len(candidates) > 0 {
			fmt.Fprintf(builder, "# Candidates for %s: %s.\n", contracts, strings.Join(candidates, ", "))
		}
	}

	for _, star := range tree {
		if len(star.Orchestrators) > 0 || len(star.Leaves) == 0 {
			continue
		}

		since := ""
		if star.Parent != "" {
			since = fmt.Sprintf(", since an orchestrator of star %s may use a branch only through its core", star.Parent)
		}

		fmt.Fprintf(builder, "# No orchestrators in %s yet: nothing may wire its leaves%s. Create one, such as %s, and build them there.\n", star.Dir, since, path.Join(star.Dir, "assembly"))
	}
}

// entryList writes a star's entries as a flow list of quoted directories,
// in the order given.
func entryList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}

	return "[" + strings.Join(quoted, ", ") + "]"
}

// joinAnd lists words as prose: a, b and c.
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
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
// unless it is a word YAML treats as a boolean or null; a lone dot, the
// snowflake's root star, is one.
var plainKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_./-]*|\.)$`)

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
