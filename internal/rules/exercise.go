package rules

import (
	"slices"

	"github.com/smarty/treaty/internal/graph"
)

const (
	ProbeAbove = "above"
	ProbeAt    = "at"
	ProbeBelow = "below"
	ProbeBlock = "block"
	ProbeFalse = "false"
	ProbeTrue  = "true"
)

// Probe is one thing instrumentation counts in a file: a block that ran (Kind
// block, with the statement lines it holds), an outcome of a decision or of
// one operand of && or || (true or false), or where a value fell against an
// exported constant it was compared with (below, at or above). Group is the
// index of the first probe of the same decision or comparison. Equality
// marks a comparison with == or !=, which needs only at and one side.
type Probe struct {
	Kind     string `json:"kind"`
	Line     int    `json:"line"`
	Lines    []int  `json:"lines,omitempty"`
	Group    int    `json:"group"`
	Operand  bool   `json:"operand,omitempty"`
	Constant string `json:"constant,omitempty"`
	Equality bool   `json:"equality,omitempty"`
}

// Exercised is what test runs show about a module or a contract: of the
// blocks, decision outcomes and boundary comparisons in its code, how many
// the tests that use it reached. Shared counts those tests whose runs
// overlapped another test's, so their reach may include the other's.
type Exercised struct {
	Blocks         int `json:"blocks"`
	BlocksRun      int `json:"blocks_run"`
	Outcomes       int `json:"outcomes"`
	OutcomesRun    int `json:"outcomes_run"`
	Boundaries     int `json:"boundaries"`
	BoundariesSeen int `json:"boundaries_seen"`
	Shared         int `json:"shared"`
}

// probeRef names one probe: its file and its index there.
type probeRef struct {
	file  string
	index int
}

// exerciser holds a graph and the runs indexed for Exercise.
type exerciser struct {
	g      *graph.Graph
	probes map[string][]Probe
	hits   map[string]map[string][]int
	shared map[string]bool
	owned  map[string][]probeRef
	calls  map[string][]string
}

// measure counts what the tests reached of a set of probes.
func (this *exerciser) measure(refs []probeRef, tests []string) (result Exercised) {
	reached := map[probeRef]bool{}
	for _, test := range tests {
		if this.shared[test] {
			result.Shared++
		}

		for file, indexes := range this.hits[test] {
			for _, index := range indexes {
				reached[probeRef{file: file, index: index}] = true
			}
		}
	}

	comparisons := map[probeRef][]Probe{}
	seen := map[probeRef]map[string]bool{}
	for _, ref := range refs {
		probe := this.probes[ref.file][ref.index]
		switch probe.Kind {
		case ProbeBlock:
			result.Blocks++
			if reached[ref] {
				result.BlocksRun++
			}
		case ProbeTrue, ProbeFalse:
			result.Outcomes++
			if reached[ref] {
				result.OutcomesRun++
			}
		default:
			group := probeRef{file: ref.file, index: probe.Group}
			comparisons[group] = append(comparisons[group], probe)
			if seen[group] == nil {
				seen[group] = map[string]bool{}
			}

			if reached[ref] {
				seen[group][probe.Kind] = true
			}
		}
	}

	for group, members := range comparisons {
		result.Boundaries++
		at, below, above := seen[group][ProbeAt], seen[group][ProbeBelow], seen[group][ProbeAbove]
		if at && (below && above || members[0].Equality && (below || above)) {
			result.BoundariesSeen++
		}
	}

	return result
}

// own assigns every probe to the function or method whose lines hold it,
// the innermost when several do.
func (this *exerciser) own() {
	byFile := map[string][]*graph.Symbol{}
	for _, symbol := range this.g.Symbols {
		if symbol.Kind == graph.KindFunction || symbol.Kind == graph.KindMethod {
			byFile[symbol.File] = append(byFile[symbol.File], symbol)
		}
	}

	for file, probes := range this.probes {
		for index, probe := range probes {
			var owner *graph.Symbol
			for _, symbol := range byFile[file] {
				if probe.Line >= symbol.Line && probe.Line <= max(symbol.EndLine, symbol.Line) && (owner == nil || symbol.Line > owner.Line) {
					owner = symbol
				}
			}

			if owner != nil {
				this.owned[owner.ID] = append(this.owned[owner.ID], probeRef{file: file, index: index})
			}
		}
	}
}

// region lists the probes of some functions and methods and of everything
// they call within a module, transitively.
func (this *exerciser) region(starts []string, module string) (result []probeRef) {
	seen := map[string]bool{}
	queue := slices.Clone(starts)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}

		seen[current] = true
		result = append(result, this.owned[current]...)
		for _, callee := range this.calls[current] {
			if symbol := this.g.Symbol(callee); symbol != nil && symbol.Module == module {
				queue = append(queue, callee)
			}
		}
	}

	return result
}

// Exercise measures what test runs reached of every module and contract.
//
// Notes:
//   - A contract's code is its own body or, for a type, its methods'
//     bodies, and every function and method they call within the module,
//     transitively. Values and interfaces have no code.
//   - Only tests that use a contract count for it, by the rules of Proof,
//     fuzz tests included; for a module, the tests that use any of its
//     top-level contracts.
//   - A comparison with an exported constant is proven when tests saw a
//     value below, at and above it, or at it and on one side for == and !=.
//
// Parameters:
//   - g: the graph of the tree the runs measured.
//   - tests: every test found in the tree.
//   - probes: each instrumented file's probes, by path.
//   - hits: by test id, the probes of each file the test reached.
//   - shared: the test ids whose runs overlapped another test's.
//
// Returns:
//   - modules: what the runs reached of each module with probes, by id.
//   - contracts: what they reached of each contract with code, by id.
func Exercise(g *graph.Graph, tests []TestUse, probes map[string][]Probe, hits map[string]map[string][]int, shared map[string]bool) (modules, contracts map[string]Exercised) {
	this := &exerciser{g: g, probes: probes, hits: hits, shared: shared, owned: map[string][]probeRef{}, calls: map[string][]string{}}
	this.own()
	for _, edge := range g.Edges {
		if edge.Kind == graph.EdgeCall {
			this.calls[edge.From] = append(this.calls[edge.From], edge.To)
		}
	}

	users := map[string][]string{}
	for _, test := range tests {
		for _, target := range test.Targets {
			users[target] = append(users[target], test.ID)
		}
	}

	members := map[string][]string{}
	for _, symbol := range g.Symbols {
		if symbol.Parent != "" {
			owner := graph.SymbolID(symbol.Module, symbol.Parent)
			members[owner] = append(members[owner], symbol.ID)
		}
	}

	modules, contracts = map[string]Exercised{}, map[string]Exercised{}
	for _, module := range g.Modules {
		var moduleTests []string
		for _, symbol := range g.SymbolsIn(module.ID) {
			if !symbol.Contract {
				continue
			}

			starts := []string{symbol.ID}
			if symbol.Kind == graph.KindType {
				starts = members[symbol.ID]
			}

			var using []string
			for _, id := range append([]string{symbol.ID}, members[symbol.ID]...) {
				using = append(using, users[id]...)
			}

			using = union(using, nil)
			if symbol.Parent == "" {
				moduleTests = append(moduleTests, using...)
			}

			if symbol.Kind != graph.KindFunction && symbol.Kind != graph.KindMethod && symbol.Kind != graph.KindType {
				continue
			}

			if refs := this.region(starts, module.ID); len(refs) > 0 {
				contracts[symbol.ID] = this.measure(refs, using)
			}
		}

		var refs []probeRef
		for _, file := range module.Files {
			for index := range probes[file] {
				refs = append(refs, probeRef{file: file, index: index})
			}
		}

		if len(refs) > 0 {
			modules[module.ID] = this.measure(refs, union(moduleTests, nil))
		}
	}

	return modules, contracts
}
