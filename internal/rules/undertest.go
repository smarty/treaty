package rules

import (
	"slices"

	"github.com/smarty/treaty/internal/graph"
)

// CodeUnderTest is the code some tests ran, file by file. Tests are the
// tests asked about and Ran those of them with a run on record.
type CodeUnderTest struct {
	Tests []string        `json:"tests"`
	Ran   []string        `json:"ran"`
	Files []FileUnderTest `json:"files"`
}

// FileUnderTest is one file's code under test: the line spans of the
// functions and methods shown, each line in them with probes, and how many
// of the file's probes in those spans the tests reached.
type FileUnderTest struct {
	File    string          `json:"file"`
	Ranges  [][2]int        `json:"ranges"`
	Lines   []LineUnderTest `json:"lines"`
	Probes  int             `json:"probes"`
	Reached int             `json:"reached"`
}

// LineUnderTest is one line's probes, and how many of them the tests
// reached. A block's probe counts on every line of the statements it holds.
type LineUnderTest struct {
	Line    int              `json:"line"`
	Probes  []ProbeUnderTest `json:"probes"`
	Reached int              `json:"reached"`
}

// ProbeUnderTest is a probe on a line, its index in the file, and the tests
// asked about that reached it.
type ProbeUnderTest struct {
	Probe
	Index int      `json:"index"`
	Tests []string `json:"tests,omitempty"`
}

// UnderTest gathers the code some tests ran: the functions and methods they
// reached, and those of the contracts they use, with what they did and did
// not reach.
//
// Notes:
//   - A used contract's code is found as for Exercise: its body or its
//     methods' bodies, and what they call within its module. It shows even
//     where nothing ran, so untested code reads as such.
//
// Parameters:
//   - g: the graph of the tree the runs measured.
//   - tests: every test found in the tree.
//   - ids: the tests asked about.
//   - probes: each instrumented file's probes, by path.
//   - hits: by test id, the probes of each file the test reached.
//
// Returns:
//   - result: the code, by file in path order.
func UnderTest(g *graph.Graph, tests []TestUse, ids []string, probes map[string][]Probe, hits map[string]map[string][]int) (result CodeUnderTest) {
	this := &exerciser{g: g, probes: probes, hits: hits, owned: map[string][]probeRef{}, calls: map[string][]string{}}
	this.own()
	for _, edge := range g.Edges {
		if edge.Kind == graph.EdgeCall {
			this.calls[edge.From] = append(this.calls[edge.From], edge.To)
		}
	}

	owner := map[probeRef]string{}
	for id, refs := range this.owned {
		for _, ref := range refs {
			owner[ref] = id
		}
	}

	members := map[string][]string{}
	for _, symbol := range g.Symbols {
		if symbol.Parent != "" {
			id := graph.SymbolID(symbol.Module, symbol.Parent)
			members[id] = append(members[id], symbol.ID)
		}
	}

	asked := map[string]bool{}
	for _, id := range ids {
		asked[id] = true
	}

	result.Tests = slices.Clone(ids)
	reachedBy := map[probeRef][]string{}
	shown := map[string]bool{}
	for _, test := range tests {
		if !asked[test.ID] {
			continue
		}

		if _, ran := hits[test.ID]; ran {
			result.Ran = append(result.Ran, test.ID)
		}

		for file, indexes := range hits[test.ID] {
			for _, index := range indexes {
				ref := probeRef{file: file, index: index}
				reachedBy[ref] = append(reachedBy[ref], test.ID)
				if id, ok := owner[ref]; ok {
					shown[id] = true
				}
			}
		}

		for _, target := range test.Targets {
			symbol := g.Symbol(target)
			if symbol == nil || !symbol.Contract {
				continue
			}

			starts := []string{symbol.ID}
			switch symbol.Kind {
			case graph.KindFunction, graph.KindMethod:
			case graph.KindType:
				starts = members[symbol.ID]
			default:
				continue
			}

			for _, ref := range this.region(starts, symbol.Module) {
				shown[owner[ref]] = true
			}
		}
	}

	slices.Sort(result.Ran)
	ranges := map[string][][2]int{}
	for id := range shown {
		if symbol := g.Symbol(id); symbol != nil {
			ranges[symbol.File] = append(ranges[symbol.File], [2]int{symbol.Line, max(symbol.EndLine, symbol.Line)})
		}
	}

	files := make([]string, 0, len(ranges))
	for file := range ranges {
		if len(probes[file]) > 0 {
			files = append(files, file)
		}
	}

	slices.Sort(files)
	for _, file := range files {
		each := FileUnderTest{File: file, Ranges: merge(ranges[file])}
		inside := func(line int) bool {
			for _, span := range each.Ranges {
				if line >= span[0] && line <= span[1] {
					return true
				}
			}

			return false
		}

		byLine := map[int]*LineUnderTest{}
		for index, probe := range probes[file] {
			lines := []int{probe.Line}
			if probe.Kind == ProbeBlock {
				lines = probe.Lines
			}

			counted := false
			by := reachedBy[probeRef{file: file, index: index}]
			slices.Sort(by)
			for _, line := range lines {
				if !inside(line) {
					continue
				}

				if !counted {
					counted = true
					each.Probes++
					if len(by) > 0 {
						each.Reached++
					}
				}

				if byLine[line] == nil {
					byLine[line] = &LineUnderTest{Line: line}
				}

				byLine[line].Probes = append(byLine[line].Probes, ProbeUnderTest{Probe: probe, Index: index, Tests: by})
				if len(by) > 0 {
					byLine[line].Reached++
				}
			}
		}

		for _, line := range byLine {
			each.Lines = append(each.Lines, *line)
		}

		slices.SortFunc(each.Lines, func(a, b LineUnderTest) int { return a.Line - b.Line })
		result.Files = append(result.Files, each)
	}

	return result
}

// merge joins overlapping and adjoining line spans, in order.
func merge(spans [][2]int) (result [][2]int) {
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	for _, span := range spans {
		if last := len(result) - 1; last >= 0 && span[0] <= result[last][1]+1 {
			result[last][1] = max(result[last][1], span[1])
			continue
		}

		result = append(result, span)
	}

	return result
}
