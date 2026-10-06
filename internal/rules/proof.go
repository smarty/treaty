package rules

import (
	"regexp"
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	TestKindExample = "example"
	TestKindFuzz    = "fuzz"
	TestKindTest    = "test"
)

var (
	// sentinelName is the convention for a sentinel error's name, such as
	// ErrNotFound or errClosed.
	sentinelName = regexp.MustCompile(`^(Err|err)([A-Z0-9_]|$)`)

	// sectionHeading starts a doc comment section, such as Returns:.
	sectionHeading = regexp.MustCompile(`^[A-Z][A-Za-z ]*:`)
)

// TestUse is one test a suite found: its id, its kind (test, fuzz or
// example) and the symbols it uses, directly or through its helpers.
type TestUse struct {
	ID      string
	Kind    string
	Targets []string
}

// ContractProof is what the tests show about one contract.
//
// Examples are the tests and examples that use it, by id; for a type, those
// that use its methods count too. Declared, Reachable and Asserted are its
// errors by symbol id: those its doc comment's Errors section lists, those
// it can return, and those a test that uses it names. A declared error
// Treaty cannot find in the repository and cannot place outside it is kept
// by the id it would have, so it shows as declared but not returned.
// External lists declared errors from outside the repository, such as
// io.EOF, which are not checked. Documented marks a contract with an Errors
// section, even an empty one.
type ContractProof struct {
	Examples   []string `json:"examples,omitempty"`
	Declared   []string `json:"declared,omitempty"`
	Reachable  []string `json:"reachable,omitempty"`
	Asserted   []string `json:"asserted,omitempty"`
	External   []string `json:"external,omitempty"`
	Documented bool     `json:"documented,omitempty"`
}

// prover holds a graph indexed for Proof.
type prover struct {
	g        *graph.Graph
	out      map[string][]graph.Edge
	members  map[string][]string
	packages map[string][]string
}

// declared resolves the errors a symbol's Errors section lists: a plain
// name in the symbol's module, and pkg.Name in the repository module of
// that package name or, when no module has it, outside the repository.
func (this *prover) declared(symbol *graph.Symbol) (ids, external []string, documented bool) {
	names, documented := errorSection(symbol.Doc)
	for _, name := range names {
		pkg, local, qualified := strings.Cut(name, ".")
		if !qualified {
			ids = append(ids, graph.SymbolID(symbol.Module, name))
			continue
		}

		modules := this.packages[pkg]
		if len(modules) == 0 {
			external = append(external, name)
			continue
		}

		id := graph.SymbolID(modules[0], local)
		for _, module := range modules {
			if this.g.Symbol(graph.SymbolID(module, local)) != nil {
				id = graph.SymbolID(module, local)
				break
			}
		}

		ids = append(ids, id)
	}

	return union(ids, nil), external, documented
}

// errors fills in a callable contract's declared, reachable and asserted
// errors.
func (this *prover) errors(symbol *graph.Symbol, tests []TestUse, proof *ContractProof) {
	proof.Declared, proof.External, proof.Documented = this.declared(symbol)
	proof.Reachable = this.reachable(symbol)
	known := union(proof.Declared, proof.Reachable)
	for _, test := range tests {
		for _, target := range test.Targets {
			if slices.Contains(known, target) && !slices.Contains(proof.Asserted, target) {
				proof.Asserted = append(proof.Asserted, target)
			}
		}
	}

	slices.Sort(proof.Asserted)
}

// examples lists the tests and examples that use a symbol or, for a type,
// one of its members.
func (this *prover) examples(symbol *graph.Symbol, users map[string][]TestUse) []string {
	var result []string
	for _, id := range append([]string{symbol.ID}, this.members[symbol.ID]...) {
		for _, test := range users[id] {
			if test.Kind != TestKindFuzz && !slices.Contains(result, test.ID) {
				result = append(result, test.ID)
			}
		}
	}

	slices.Sort(result)
	return result
}

// reachable lists the errors a callable symbol can return.
func (this *prover) reachable(start *graph.Symbol) []string {
	found := map[string]bool{}
	seen := map[string]bool{start.ID: true}
	stack := []string{start.ID}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, edge := range this.out[current] {
			target := this.g.Symbol(edge.To)
			switch {
			case target == nil:
			case isError(target):
				if !edge.Compare && (target.Kind == graph.KindValue || edge.Literal) {
					found[target.ID] = true
				}
			case target.Kind != graph.KindFunction && target.Kind != graph.KindMethod, edge.Handled, edge.Compare, seen[target.ID]:
			default:
				seen[target.ID] = true
				if target.Module != start.Module {
					if ids, _, documented := this.declared(target); documented {
						for _, id := range ids {
							if this.g.Symbol(id) != nil {
								found[id] = true
							}
						}

						continue
					}
				}

				stack = append(stack, target.ID)
			}
		}
	}

	var result []string
	for id := range found {
		result = append(result, id)
	}

	slices.Sort(result)
	return result
}

// Proof finds what the tests show about every contract in the modules whose
// language has tests Treaty can find, and adds the counts to each module's
// metrics.
//
// Notes:
//   - An example is a test or an example that uses a contract; a fuzz test
//     states a property, not an example, so it counts only for errors.
//   - A contract can return the sentinel errors (named Err... or err...) it
//     names, other than to compare them, and the error types (named
//     ...Error) it builds as literals. A call whose results it handles,
//     never returning them, adds nothing. Within its own module every other
//     call is followed. A call into another module takes the callee's Errors
//     section when it has one, and is followed when it does not.
//   - An error is asserted when a test that uses the contract names it,
//     which cannot tell errors.Is(err, ErrX) from any other mention.
//
// Parameters:
//   - g: the graph to measure.
//   - tests: every test found in g's tree.
//   - measured: the languages whose tests can be found, such as go.
//   - metrics: the modules' metrics, which gain the counts.
//
// Returns:
//   - result: the proof of every contract in a measured module, by id.
func Proof(g *graph.Graph, tests []TestUse, measured map[string]bool, metrics map[string]Metric) (result map[string]ContractProof) {
	this := &prover{g: g, out: map[string][]graph.Edge{}, members: map[string][]string{}, packages: map[string][]string{}}
	for _, edge := range g.Edges {
		this.out[edge.From] = append(this.out[edge.From], edge)
	}

	for _, symbol := range g.Symbols {
		if symbol.Parent != "" {
			owner := graph.SymbolID(symbol.Module, symbol.Parent)
			this.members[owner] = append(this.members[owner], symbol.ID)
		}
	}

	for _, module := range g.Modules {
		if module.Name != "" {
			this.packages[module.Name] = append(this.packages[module.Name], module.ID)
		}
	}

	users := map[string][]TestUse{}
	for _, test := range tests {
		for _, target := range test.Targets {
			users[target] = append(users[target], test)
		}
	}

	result = map[string]ContractProof{}
	for _, module := range g.Modules {
		metric := metrics[module.ID]
		metric.Measured = measured[module.Language]
		if !metric.Measured {
			metrics[module.ID] = metric
			continue
		}

		examples := map[string]bool{}
		for _, symbol := range g.SymbolsIn(module.ID) {
			if !symbol.Contract {
				continue
			}

			proof := ContractProof{Examples: this.examples(symbol, users)}
			if symbol.Kind == graph.KindFunction || symbol.Kind == graph.KindMethod {
				this.errors(symbol, users[symbol.ID], &proof)
				known := union(proof.Declared, proof.Reachable)
				metric.Errors += len(known)
				metric.ErrorsProven += len(intersect(known, proof.Asserted))
				metric.Undeclared += len(subtract(proof.Reachable, proof.Declared))
				metric.Unreturned += len(subtract(proof.Declared, proof.Reachable))
			}

			if symbol.Parent == "" {
				metric.Contracts++
				if len(proof.Examples) > 0 {
					metric.ExampleContracts++
				}

				for _, id := range proof.Examples {
					examples[id] = true
				}
			}

			result[symbol.ID] = proof
		}

		metric.Examples = len(examples)
		metrics[module.ID] = metric
	}

	return result
}

// errorSection reads the names a doc comment's Errors section lists, one per
// "- Name:" line, and whether it has the section. The section ends at a
// blank line after its first entry or at the next section heading.
func errorSection(doc string) (names []string, found bool) {
	for line := range strings.SplitSeq(doc, "\n") {
		text := strings.TrimSpace(line)
		if !found {
			found = text == "Errors:" || strings.HasPrefix(text, "Errors: ")
			continue
		}

		if text == "" && len(names) > 0 || sectionHeading.MatchString(text) {
			break
		}

		entry, ok := strings.CutPrefix(text, "- ")
		if !ok {
			continue
		}

		name, _, _ := strings.Cut(entry, ":")
		name = strings.Trim(strings.TrimSpace(name), "`*&")
		if name != "" && !strings.ContainsAny(name, " \t") {
			names = append(names, name)
		}
	}

	return names, found
}

// intersect lists the ids in both lists.
func intersect(a, b []string) (result []string) {
	for _, id := range a {
		if slices.Contains(b, id) {
			result = append(result, id)
		}
	}

	return result
}

// isError reports whether a symbol is an error by Go's naming conventions:
// a sentinel value named Err... or err..., or a type named ...Error.
func isError(symbol *graph.Symbol) bool {
	if symbol.Parent != "" {
		return false
	}

	return symbol.Kind == graph.KindValue && sentinelName.MatchString(symbol.Name) || symbol.Kind == graph.KindType && strings.HasSuffix(symbol.Name, "Error")
}

// subtract lists the ids in a that are not in b.
func subtract(a, b []string) (result []string) {
	for _, id := range a {
		if !slices.Contains(b, id) {
			result = append(result, id)
		}
	}

	return result
}

// union lists the ids in either list once, sorted.
func union(a, b []string) []string {
	result := slices.Concat(a, b)
	slices.Sort(result)
	return slices.Compact(result)
}
