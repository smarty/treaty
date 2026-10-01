package app

import (
	"fmt"
	"path"
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
//   - For slices and modular, the slices or contexts are the children of the
//     directory whose children depend on each other least. For slices, a
//     child that other children use becomes shared.
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
//   - rules.ErrArchitecture: style is not one of rules.Styles.
func (this *Service) Init(style string) (path string, err error) {
	if style == "" {
		style = rules.StyleNone
	}

	if !slices.Contains(rules.Styles, style) {
		return "", fmt.Errorf("%w: unknown architecture %q; use one of %s", rules.ErrArchitecture, style, strings.Join(rules.Styles, ", "))
	}

	if err := this.workspace.Init(); err != nil {
		return "", err
	}

	g, err := this.extractor.Extract(this.root)
	if err != nil {
		return "", err
	}

	_, text := propose(g, style)
	return this.workspace.WriteConfigDraft(text)
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

	_, text := propose(g, style)
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
	case rules.StyleSlices, rules.StyleModular:
		result.Shared = groups[graph.LayerShared]
		if root != "" {
			result.Slices = []string{strings.TrimPrefix(root+"/*", "./")}
		}

		if style == rules.StyleModular {
			result.Public = []string{"."}
		}
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
		builder.WriteString("architecture: hexagonal\n")
		builder.WriteString("layers:\n")
		for _, group := range []string{graph.LayerComposition, graph.LayerDomain, graph.LayerApplication} {
			fmt.Fprintf(&builder, "  %s: %s\n", group, yamlList(groups[group]))
		}

		builder.WriteString("  adapter:\n")
		fmt.Fprintf(&builder, "    driving: %s\n", yamlList(groups[graph.SideDriving]))
		fmt.Fprintf(&builder, "    driven: %s\n", yamlList(groups[graph.SideDriven]))
		builder.WriteString("rules:\n  fail_on: [breaking, layer_violation]\n  warn_on: [unclassified]\n")
		return builder.String()
	}

	fmt.Fprintf(&builder, "architecture: %s\n", style)
	fmt.Fprintf(&builder, "composition: %s\n", yamlList(groups[graph.LayerComposition]))
	if style == rules.StyleClean {
		builder.WriteString("layers: # outermost first; each may use itself and every ring inside it\n")
	} else {
		builder.WriteString("layers: # top to bottom; each may use itself and every layer below it\n")
	}

	for _, name := range outsideIn(style) {
		fmt.Fprintf(&builder, "  %s: %s\n", name, yamlList(groups[name]))
	}

	builder.WriteString("rules:\n  fail_on: [breaking, layer_violation]\n  warn_on: [unclassified]\n")
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
	case rules.StyleSlices, rules.StyleModular:
		root, proposal := proposeSections(g, style)
		return architectureFrom(style, root, proposal), sectionDraft(style, root, proposal)
	default:
		proposal := proposeLayers(g, style)
		return architectureFrom(style, "", proposal), layerDraft(style, proposal)
	}
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

// proposeSections finds the directory whose children make the best slices
// or contexts, and places every module around them.
//
// Notes:
//   - A candidate is any directory with two or more children holding
//     modules that are not programs. Its score is the number of children
//     that can stand alone: for slices, a child that another child uses
//     becomes shared instead; for contexts, a child whose internal packages
//     another child uses counts against it. The highest score wins, then a
//     name such as features or modules, then the shallower directory.
//   - Outside the chosen directory, programs and modules that use a slice
//     or context are composition, and modules only used by them are
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
	builder.WriteString("# treaty init --architecture <hexagonal, clean, layered, slices or modular>.\n")
	builder.WriteString("architecture: none\n")
	builder.WriteString("rules:\n  fail_on: [breaking]\n")
	return builder.String()
}

// sectionDraft writes the proposed config for slices or modular.
func sectionDraft(style, root string, proposal map[string]string) string {
	groups := map[string][]string{}
	for _, glob := range compressGlobs(proposal) {
		groups[glob.group] = append(groups[glob.group], glob.pattern)
	}

	unit, key := "slice", "slices"
	if style == rules.StyleModular {
		unit, key = "context", "contexts"
	}

	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	fmt.Fprintf(&builder, "# Each child of the directory whose children depend on each other least is a %s.\n", unit)
	if style == rules.StyleSlices {
		builder.WriteString("# Children that other slices use are shared. Run treaty check to see what is left.\n")
	} else {
		builder.WriteString("# Other contexts may use only a context's public packages. Run treaty check to\n# see which internals are used across contexts.\n")
	}

	writeUnplaced(&builder, proposal, "neither only using nor only used by the "+key)
	fmt.Fprintf(&builder, "architecture: %s\n", style)
	fmt.Fprintf(&builder, "composition: %s\n", yamlList(groups[graph.LayerComposition]))
	fmt.Fprintf(&builder, "shared: %s\n", yamlList(groups[graph.LayerShared]))
	if root == "" {
		fmt.Fprintf(&builder, "# No directory has two independent children; list the %s by hand.\n", key)
		fmt.Fprintf(&builder, "%s: []\n", key)
	} else {
		fmt.Fprintf(&builder, "%s: %s # each match is one %s\n", key, yamlList([]string{strings.TrimPrefix(root+"/*", "./")}), unit)
	}

	if style == rules.StyleSlices {
		builder.WriteString("# Optional layers inside every slice, top to bottom, with globs relative to the\n# slice root; each may use itself and the layers below it:\n")
		builder.WriteString("# layers:\n#   api: [\".\"]\n#   app: [\"app/**\"]\n#   store: [\"store/**\"]\n")
	} else {
		builder.WriteString("public: [\".\"] # packages inside each context, relative to its root, that other contexts may use\n")
	}

	builder.WriteString("rules:\n  fail_on: [breaking, layer_violation, cycle]\n  warn_on: [unclassified]\n")
	return builder.String()
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
