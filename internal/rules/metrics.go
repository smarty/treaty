package rules

import (
	"math"

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
		metric := Metric{Afferent: afferent[module.ID], Efferent: efferent[module.ID]}
		if total := metric.Afferent + metric.Efferent; total > 0 {
			metric.Instability = round(float64(metric.Efferent) / float64(total))
		}

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

		if contracts > 0 {
			metric.Abstractness = round(float64(interfaces) / float64(contracts))
		}

		metric.Distance = round(math.Abs(metric.Abstractness + metric.Instability - 1))
		result[module.ID] = metric
	}

	return result
}

func round(value float64) float64 {
	return math.Round(value*100) / 100
}
