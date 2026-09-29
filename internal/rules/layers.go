// Package rules holds the mechanical checks: layer assignment and rules,
// stability metrics, contract change classification and review ranking.
package rules

import (
	"path"
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

// Layers maps each layer to the path globs whose modules belong to it.
// Composition wires adapters into the core: it is the only layer that may
// depend on adapters.
type Layers struct {
	Composition []string
	Domain      []string
	Application []string
	Driving     []string
	Driven      []string
}

// Violation is a module edge that points outward.
type Violation struct {
	From       string       `json:"from"`
	FromLayer  string       `json:"from_layer"`
	To         string       `json:"to"`
	ToLayer    string       `json:"to_layer"`
	References []graph.Edge `json:"references"`
}

// Assign places every module of the graph into a layer, using the first glob
// that matches its path. Modules that match nothing become Unclassified.
//
// Parameters:
//   - g: the graph whose modules are assigned.
func (this Layers) Assign(g *graph.Graph) {
	for _, module := range g.Modules {
		module.Layer, module.Side = this.Resolve(module.Path)
	}
}

// Resolve finds the layer and side for one module path.
//
// Parameters:
//   - modulePath: the module path relative to the repository root.
//
// Returns:
//   - layer: the layer name.
//   - side: driving or driven for adapters, empty otherwise.
func (this Layers) Resolve(modulePath string) (layer string, side string) {
	switch {
	case MatchAny(this.Composition, modulePath):
		return graph.LayerComposition, ""
	case MatchAny(this.Domain, modulePath):
		return graph.LayerDomain, ""
	case MatchAny(this.Application, modulePath):
		return graph.LayerApplication, ""
	case MatchAny(this.Driving, modulePath):
		return graph.LayerAdapter, graph.SideDriving
	case MatchAny(this.Driven, modulePath):
		return graph.LayerAdapter, graph.SideDriven
	default:
		return graph.LayerUnclassified, ""
	}
}

// Allowed reports whether a module in one layer may depend on a module in
// another. Composition may depend on every layer, and nothing inside the
// hexagon may depend on composition. Unclassified modules are reported
// separately, never as violations.
//
// Parameters:
//   - from: the source module's layer.
//   - to: the target module's layer.
//
// Returns:
//   - result: true when the dependency points inward or sideways.
func Allowed(from, to string) bool {
	if to == graph.LayerComposition && from != graph.LayerComposition && from != graph.LayerUnclassified {
		return false
	}

	if from == graph.LayerUnclassified || to == graph.LayerUnclassified {
		return true
	}

	return slices.Contains(AllowedLayers(from), to)
}

// AllowedLayers lists the layers a module in the given layer may depend on.
//
// Parameters:
//   - layer: the module's layer.
//
// Returns:
//   - result: layer names, innermost first.
func AllowedLayers(layer string) []string {
	switch layer {
	case graph.LayerDomain:
		return []string{graph.LayerDomain}
	case graph.LayerApplication, graph.LayerAdapter:
		return []string{graph.LayerDomain, graph.LayerApplication}
	case graph.LayerComposition:
		return []string{graph.LayerDomain, graph.LayerApplication, graph.LayerAdapter, graph.LayerComposition}
	default:
		return []string{graph.LayerDomain, graph.LayerApplication, graph.LayerAdapter}
	}
}

// Match reports whether a path matches a glob. A "**" segment matches zero
// or more path segments; other segments use path.Match rules.
//
// Parameters:
//   - glob: the pattern, such as internal/graph/**.
//   - value: the path to test.
//
// Returns:
//   - result: true on a match.
func Match(glob, value string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(value, "/"))
}

// MatchAny reports whether a path matches any of the globs.
//
// Parameters:
//   - globs: the patterns.
//   - value: the path to test.
//
// Returns:
//   - result: true when at least one glob matches.
func MatchAny(globs []string, value string) bool {
	for _, glob := range globs {
		if Match(glob, value) {
			return true
		}
	}

	return false
}

// LayerRank orders layers from the center outward.
//
// Parameters:
//   - layer: the layer name.
//
// Returns:
//   - result: 0 for domain, 1 for application, 2 for adapter, 3 for
//     composition, 4 otherwise.
func LayerRank(layer string) int {
	switch layer {
	case graph.LayerDomain:
		return 0
	case graph.LayerApplication:
		return 1
	case graph.LayerAdapter:
		return 2
	case graph.LayerComposition:
		return 3
	default:
		return 4
	}
}

// Violations finds every module edge that breaks the layer rules.
//
// Parameters:
//   - g: a graph whose modules have been assigned layers.
//
// Returns:
//   - result: the violations, sorted by source and target.
func Violations(g *graph.Graph) []Violation {
	var result []Violation
	for _, edge := range g.ModuleEdges() {
		from, to := g.Module(edge.From), g.Module(edge.To)
		if Allowed(from.Layer, to.Layer) {
			continue
		}

		result = append(result, Violation{
			From: edge.From, FromLayer: from.Layer,
			To: edge.To, ToLayer: to.Layer,
			References: edge.References,
		})
	}

	return result
}

func matchSegments(glob, value []string) bool {
	if len(glob) == 0 {
		return len(value) == 0
	}

	if glob[0] == "**" {
		for i := 0; i <= len(value); i++ {
			if matchSegments(glob[1:], value[i:]) {
				return true
			}
		}

		return false
	}

	if len(value) == 0 {
		return false
	}

	ok, err := path.Match(glob[0], value[0])
	return err == nil && ok && matchSegments(glob[1:], value[1:])
}
