package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

// Init creates the workspace and proposes a layer config from the shape of
// the import graph, for a person to edit.
//
// Notes:
//   - Programs, such as Go main packages, and other modules nothing imports
//     are composition. A module that imports nothing in the repository but
//     is used is domain, since a leaf can always sit innermost. Every other
//     module sits one ring further in than the deepest module importing it:
//     adapters are imported only by composition, application only from the
//     adapter ring outward, and everything deeper is domain. So the proposal
//     has no layer violations by construction.
//   - A library module with no imports either way has no shape to place it
//     by. It is left unclassified and listed for a person to place.
//   - Directory names are hints. A hinted layer replaces the shape's answer
//     only when it adds no violation, and it decides an adapter's side.
//
// Returns:
//   - path: where the draft config was written.
//   - err: the tree could not be read or the workspace could not be written.
func (this *Service) Init() (path string, err error) {
	if err := this.workspace.Init(); err != nil {
		return "", err
	}

	g, err := this.extractor.Extract(this.root)
	if err != nil {
		return "", err
	}

	groups := map[string][]string{}
	proposal := proposeLayers(g)
	for _, glob := range compressGlobs(proposal) {
		groups[glob.group] = append(groups[glob.group], glob.pattern)
	}

	var unplaced []string
	for modulePath, group := range proposal {
		if group == "" {
			unplaced = append(unplaced, modulePath)
		}
	}

	sort.Strings(unplaced)

	var builder strings.Builder
	builder.WriteString("# Proposed by treaty init from the import graph. Edit before relying on it.\n")
	builder.WriteString("# Modules nothing imports are composition; the rest sit one ring inside the\n")
	builder.WriteString("# deepest module that imports them. Directory names only break ties.\n")
	if len(unplaced) > 0 {
		fmt.Fprintf(&builder, "# Unclassified, with no imports either way to place them by: %s\n", strings.Join(unplaced, ", "))
	}

	builder.WriteString("layers:\n")
	for _, group := range []string{"composition", "domain", "application"} {
		fmt.Fprintf(&builder, "  %s: %s\n", group, yamlList(groups[group]))
	}

	builder.WriteString("  adapter:\n")
	fmt.Fprintf(&builder, "    driving: %s\n", yamlList(groups["driving"]))
	fmt.Fprintf(&builder, "    driven: %s\n", yamlList(groups["driven"]))
	builder.WriteString("rules:\n  fail_on: [breaking, layer_violation]\n  warn_on: [unclassified, weak_contract]\n  strength_threshold: 0.60\n")
	builder.WriteString("mutation:\n  scope: blast_radius\n  timeout_per_mutant: 30s\n")
	return this.workspace.WriteConfigDraft(builder.String())
}

// glob is one proposed config line: a path pattern and its layer group.
type glob struct {
	group   string
	pattern string
}

// compressGlobs turns a proposal keyed by module path into globs, using
// dir/** for the highest directory whose modules all share one group.
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

		if entry := (glob{group, pattern}); group != "" && !seen[entry] {
			seen[entry] = true
			result = append(result, entry)
		}
	}

	return result
}

// proposeLayers places every module of an unlayered graph by the shape of
// its imports, then applies directory-name hints that keep it valid.
//
// Returns:
//   - result: composition, domain, application, driving or driven, keyed by
//     module path; empty for a module left unclassified.
func proposeLayers(g *graph.Graph) map[string]string {
	const deepest = 3
	importers, imports := map[string][]string{}, map[string][]string{}
	for _, edge := range g.ModuleEdges() {
		importers[edge.To] = append(importers[edge.To], edge.From)
		imports[edge.From] = append(imports[edge.From], edge.To)
	}

	// Longest path from the entry points, capped at the domain ring so that
	// cycles settle.
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

	layers := map[string]string{}
	for _, module := range g.Modules {
		switch {
		case module.Entry:
			layers[module.ID] = graph.LayerComposition
		case len(importers[module.ID]) == 0 && len(imports[module.ID]) == 0:
			layers[module.ID] = graph.LayerUnclassified
		case len(imports[module.ID]) == 0:
			layers[module.ID] = graph.LayerDomain
		default:
			layers[module.ID] = []string{graph.LayerComposition, graph.LayerAdapter, graph.LayerApplication, graph.LayerDomain}[level[module.ID]]
		}
	}

	valid := func(id, layer string) bool {
		for _, other := range imports[id] {
			if !rules.Allowed(layer, layers[other]) {
				return false
			}
		}

		for _, other := range importers[id] {
			if !rules.Allowed(layers[other], layer) {
				return false
			}
		}

		return true
	}

	sides := map[string]string{}
	for _, module := range g.Modules {
		hint := guessLayer(module.Path)
		layer := hint
		if hint == "driving" || hint == "driven" {
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

func guessLayer(path string) string {
	segments := strings.Split(strings.ToLower(path), "/")
	has := func(names ...string) bool {
		for _, segment := range segments {
			for _, name := range names {
				if segment == name {
					return true
				}
			}
		}

		return false
	}

	switch {
	case has("cmd", "main"):
		return "composition"
	case has("domain", "model", "entity", "entities", "core"):
		return "domain"
	case has("cli", "http", "api", "web", "grpc", "mcp", "server", "handlers"):
		return "driving"
	case has("adapter", "adapters", "infra", "infrastructure", "storage", "db", "database", "repository"):
		return "driven"
	case has("app", "application", "ports", "usecase", "usecases", "service", "services"):
		return "application"
	default:
		return ""
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
