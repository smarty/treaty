package rules

import (
	"math"
	"sort"

	"github.com/smarty/treaty/internal/graph"
)

// Metric is Robert Martin's package metrics for one module.
type Metric struct {
	Afferent     int     `json:"ca"`
	Efferent     int     `json:"ce"`
	Instability  float64 `json:"instability"`
	Abstractness float64 `json:"abstractness"`
	Distance     float64 `json:"distance"`

	// Measured marks a module whose language has tests Treaty can find;
	// the counts below are only meaningful when it is set. Contracts are
	// its top-level contracts and ExampleContracts those with an example;
	// Examples counts the distinct tests and examples that use them.
	// Errors counts each (contract, error) pair declared or returned,
	// ErrorsProven those a test names, Undeclared those returned but not
	// declared, and Unreturned those declared but not returned. See Proof.
	Measured         bool `json:"measured"`
	Contracts        int  `json:"contracts"`
	ExampleContracts int  `json:"example_contracts"`
	Examples         int  `json:"examples"`
	Errors           int  `json:"errors"`
	ErrorsProven     int  `json:"errors_proven"`
	Undeclared       int  `json:"undeclared"`
	Unreturned       int  `json:"unreturned"`
}

// Metrics computes stability metrics for every module, counting afferent and
// efferent coupling as distinct modules.
//
// Notes:
//   - A module with no coupling at all has instability 0.
//   - A module with no contract symbols has abstractness 0.
//
// Parameters:
//   - g: the graph to measure.
//
// Returns:
//   - result: metrics keyed by module id.
func Metrics(g *graph.Graph) map[string]Metric {
	afferent := map[string]int{}
	efferent := map[string]int{}
	for _, edge := range g.ModuleEdges() {
		efferent[edge.From]++
		afferent[edge.To]++
	}

	result := map[string]Metric{}
	for _, module := range g.Modules {
		interfaces, contracts := 0, 0
		for _, symbol := range g.SymbolsIn(module.ID) {
			if !symbol.Contract || symbol.Parent != "" {
				continue
			}

			contracts++
			if symbol.Kind == graph.KindInterface {
				interfaces++
			}
		}

		result[module.ID] = measure(afferent[module.ID], efferent[module.ID], contracts, interfaces)
	}

	return result
}

// GroupMetric measures several modules taken together as one, as Metrics
// measures a module: coupling counts the distinct modules outside the group
// that depend on it and that it depends on, and abstractness pools the
// members' top-level contracts. Edges between members do not count.
//
// Parameters:
//   - edges: every module edge.
//   - members: the ids of the group's modules.
//   - contracts: the members' top-level contracts.
//   - interfaces: how many of those are interfaces.
//
// Returns:
//   - metric: the group's coupling, instability, abstractness and distance.
//   - uses: the modules outside the group it depends on, sorted.
//   - usedBy: the modules outside the group that depend on it, sorted.
func GroupMetric(edges []graph.ModuleEdge, members []string, contracts, interfaces int) (metric Metric, uses, usedBy []string) {
	inside := map[string]bool{}
	for _, id := range members {
		inside[id] = true
	}

	out, in := map[string]bool{}, map[string]bool{}
	for _, edge := range edges {
		switch {
		case inside[edge.From] && !inside[edge.To]:
			out[edge.To] = true
		case inside[edge.To] && !inside[edge.From]:
			in[edge.From] = true
		}
	}

	uses, usedBy = sortedSet(out), sortedSet(in)
	return measure(len(usedBy), len(uses), contracts, interfaces), uses, usedBy
}

// measure computes the stability metrics from coupling and contract
// counts.
func measure(afferent, efferent, contracts, interfaces int) Metric {
	metric := Metric{Afferent: afferent, Efferent: efferent}
	if total := afferent + efferent; total > 0 {
		metric.Instability = round(float64(efferent) / float64(total))
	}

	if contracts > 0 {
		metric.Abstractness = round(float64(interfaces) / float64(contracts))
	}

	metric.Distance = round(math.Abs(metric.Abstractness + metric.Instability - 1))
	return metric
}

func sortedSet(set map[string]bool) []string {
	result := make([]string, 0, len(set))
	for key := range set {
		result = append(result, key)
	}

	sort.Strings(result)
	return result
}

func round(value float64) float64 {
	return math.Round(value*100) / 100
}
