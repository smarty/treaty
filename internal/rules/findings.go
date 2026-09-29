package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	FindingBreaking       = "breaking"
	FindingCompatible     = "compatible"
	FindingCycle          = "cycle"
	FindingImplementation = "implementation"
	FindingImplementers   = "implementers"
	FindingLayerViolation = "layer_violation"
	FindingMoved          = "moved"
	FindingNewContract    = "new_contract"
	FindingNewModule      = "new_module"
	FindingUnclassified   = "unclassified"
	FindingVariants       = "variant_mismatch"

	SeverityHigh   = "high"
	SeverityLow    = "low"
	SeverityMedium = "medium"
)

// Finding is one item in the review queue.
type Finding struct {
	Severity string   `json:"severity"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Targets  []string `json:"targets"`
	Blast    int      `json:"blast_radius"`
}

// BlastRadius counts the contract symbols that transitively depend on each
// symbol through reference edges.
//
// Parameters:
//   - g: the graph to walk.
//
// Returns:
//   - result: counts keyed by symbol id.
func BlastRadius(g *graph.Graph) map[string]int {
	callers := map[string][]string{}
	for _, edge := range g.Edges {
		callers[edge.To] = append(callers[edge.To], edge.From)
	}

	result := map[string]int{}
	for _, symbol := range g.Symbols {
		seen := map[string]bool{symbol.ID: true}
		queue := []string{symbol.ID}
		count := 0
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, caller := range callers[current] {
				if seen[caller] {
					continue
				}

				seen[caller] = true
				queue = append(queue, caller)
				if s := g.Symbol(caller); s != nil && s.Contract {
					count++
				}
			}
		}

		result[symbol.ID] = count
	}

	return result
}

// Rank builds the review queue from every check's output, sorted by
// severity, then by blast radius.
//
// Parameters:
//   - head: the graph after the change, with layers assigned.
//   - base: the graph before the change, or nil when there is no diff.
//   - changes: the classified symbol changes.
//   - violations: the layer violations in head.
//
// Returns:
//   - result: the ranked findings.
func Rank(head, base *graph.Graph, changes []Change, violations []Violation) []Finding {
	blast := BlastRadius(head)
	result := []Finding{}
	for _, violation := range violations {
		ref := violation.References[0]
		title := "%s → %s breaks a layer rule"
		if violation.Kind == FindingCycle {
			title = "%s → %s closes a cycle"
		}

		result = append(result, Finding{
			Severity: SeverityHigh, Kind: violation.Kind,
			Title:   fmt.Sprintf(title, violation.From, violation.To),
			Detail:  fmt.Sprintf("%s; %d reference(s), first %s → %s at %s:%d", violation.Rule, len(violation.References), ref.From, ref.To, ref.File, ref.Line),
			Targets: []string{violation.From, violation.To},
			Blast:   blast[ref.To],
		})
	}

	for _, module := range head.Modules {
		if module.Layer == graph.LayerUnclassified {
			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingUnclassified,
				Title:   fmt.Sprintf("%s matches no layer", module.ID),
				Detail:  "No glob in treaty.yaml matches this module, so its dependencies are not checked.",
				Targets: []string{module.ID},
			})
		}

		if base != nil && base.Module(module.ID) == nil {
			detail := fmt.Sprintf("Placed in the %s layer.", module.Layer)
			if module.Slice != "" {
				detail = fmt.Sprintf("Placed in the %s layer of %s.", module.Layer, module.Slice)
			}

			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingNewModule,
				Title:   fmt.Sprintf("New module %s", module.ID),
				Detail:  detail,
				Targets: []string{module.ID},
			})
		}
	}

	for _, symbol := range head.Symbols {
		for _, variant := range symbol.Variants {
			if variant.Signature == "" || (variant.Signature == symbol.Signature && fieldsEqual(variant.Fields, symbol.Fields)) {
				continue
			}

			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingVariants,
				Title:   fmt.Sprintf("Build variants of %s disagree", symbol.ID),
				Detail:  fmt.Sprintf("%s:%d declares %s; %s:%d declares %s", symbol.File, symbol.Line, symbol.Signature, variant.File, variant.Line, variant.Signature),
				Targets: []string{symbol.ID},
				Blast:   blast[symbol.ID],
			})
		}
	}

	var implementation []string
	for _, change := range changes {
		symbol := head.Symbol(change.Symbol)
		switch change.Kind {
		case ChangeBreaking, ChangeRemoved:
			finding := Finding{
				Severity: SeverityHigh, Kind: FindingBreaking,
				Title:   fmt.Sprintf("Breaking change to %s", change.Symbol),
				Detail:  describe(change),
				Targets: []string{change.Symbol},
				Blast:   blast[change.Symbol],
			}

			if symbol != nil && symbol.Kind == graph.KindInterface && change.Before == "" {
				finding.Severity, finding.Kind = SeverityMedium, FindingImplementers
				finding.Title = fmt.Sprintf("Interface %s gained methods", change.Symbol)
				finding.Detail = "Existing implementers no longer satisfy it."
			}

			result = append(result, finding)
		case ChangeAdded:
			if symbol != nil && symbol.Contract {
				result = append(result, Finding{
					Severity: SeverityMedium, Kind: FindingNewContract,
					Title:   fmt.Sprintf("New contract %s", change.Symbol),
					Detail:  symbol.Signature,
					Targets: []string{change.Symbol},
					Blast:   blast[change.Symbol],
				})
			}
		case ChangeMoved:
			verb := "moved to"
			if from, _ := graph.SplitSymbolID(change.From); from == change.Module {
				verb = "renamed to"
			}

			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingMoved,
				Title:   fmt.Sprintf("%s %s %s", change.From, verb, change.Symbol),
				Detail:  describe(change),
				Targets: []string{change.Symbol},
				Blast:   blast[change.Symbol],
			})
		case ChangeContract:
			result = append(result, Finding{
				Severity: SeverityLow, Kind: FindingCompatible,
				Title:   fmt.Sprintf("Compatible change to %s", change.Symbol),
				Detail:  describe(change),
				Targets: []string{change.Symbol},
				Blast:   blast[change.Symbol],
			})
		case ChangeImplementation:
			implementation = append(implementation, change.Symbol)
		}
	}

	if len(implementation) > 0 {
		result = append(result, Finding{
			Severity: SeverityLow, Kind: FindingImplementation,
			Title:   fmt.Sprintf("%d implementation-only change(s)", len(implementation)),
			Detail:  strings.Join(implementation, ", "),
			Targets: implementation,
		})
	}

	sort.SliceStable(result, func(i, j int) bool {
		if severityRank(result[i].Severity) != severityRank(result[j].Severity) {
			return severityRank(result[i].Severity) < severityRank(result[j].Severity)
		}

		if result[i].Blast != result[j].Blast {
			return result[i].Blast > result[j].Blast
		}

		return result[i].Title < result[j].Title
	})
	return result
}

func describe(change Change) string {
	var result string
	switch {
	case change.Kind == ChangeRemoved:
		result = "Removed: " + change.Before
	case change.Before != "":
		result = fmt.Sprintf("%s → %s", change.Before, change.After)
	default:
		result = change.After
	}

	if len(change.FieldsRemoved) > 0 {
		result += "; fields removed: " + strings.Join(change.FieldsRemoved, "; ")
	}

	if len(change.FieldsAdded) > 0 {
		result += "; fields added: " + strings.Join(change.FieldsAdded, "; ")
	}

	return result
}

func fieldsEqual(a, b []graph.Field) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i].Text != b[i].Text {
			return false
		}
	}

	return true
}

func severityRank(severity string) int {
	switch severity {
	case SeverityHigh:
		return 0
	case SeverityMedium:
		return 1
	default:
		return 2
	}
}
