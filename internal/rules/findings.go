package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	FindingAmbiguous      = "ambiguous_boundary"
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

	VerdictFail = "fail"
	VerdictWarn = "warn"

	SeverityHigh   = "high"
	SeverityLow    = "low"
	SeverityMedium = "medium"
)

var (
	// FindingKinds lists every kind of finding.
	FindingKinds = []string{FindingAmbiguous, FindingBreaking, FindingCompatible, FindingCycle, FindingImplementation, FindingImplementers, FindingLayerViolation, FindingMoved, FindingNewContract, FindingNewModule, FindingUnclassified, FindingVariants}

	// Failable lists the kinds of finding that can fail a check, or warn.
	Failable = []string{FindingBreaking, FindingLayerViolation, FindingCycle, FindingUnclassified, FindingAmbiguous}
)

// Finding is one item in the review queue. File and Line say where it
// shows in the code, when it has one place: a file alone for a module or a
// removed symbol. Existing marks one the base already had. Verdict is fail
// or warn when the finding counts toward the check's failures or warnings,
// as rules.fail_on and rules.warn_on say.
//
// Kind and Target identify the finding across edits: Target is the symbol
// or module it is about, or for a module edge "from -> to". Fingerprint
// sums up what it says, from the data behind it and never its lines, so it
// changes when the finding does. An implementation-only finding gathers
// many symbols and has no target. Ignored is the reason treaty.yaml gives
// for ignoring it; WasIgnored is that reason when treaty.yaml ignored it
// with another fingerprint, so it counts again until it is ignored anew.
type Finding struct {
	Severity string   `json:"severity"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Targets  []string `json:"targets"`
	Blast    int      `json:"blast_radius"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Existing bool     `json:"existing,omitempty"`
	Verdict  string   `json:"verdict,omitempty"`

	Target      string `json:"target,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Ignored     string `json:"ignored,omitempty"`
	WasIgnored  string `json:"was_ignored,omitempty"`
}

// EdgeTarget names a module edge as a finding's target.
func EdgeTarget(from, to string) string { return from + " -> " + to }

// fingerprint sums up a finding's substance in twelve hex digits.
func fingerprint(kind string, parts ...string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:6])
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
		file, line, first := violation.First()
		blastTarget := violation.To
		if len(violation.References) > 0 {
			blastTarget = violation.References[0].To
		}

		title := "%s → %s breaks a layer rule"
		if violation.Kind == FindingCycle {
			title = "%s → %s closes a cycle"
		}

		// A violation the base already had is debt, not this change's
		// doing: it ranks below the ones the change introduced.
		severity, existing := SeverityHigh, ""
		if violation.Existing {
			severity, existing = SeverityMedium, "Existing: the base has it too, so it does not fail the check. "
			title += " (existing)"
		}

		result = append(result, Finding{
			Severity: severity, Kind: violation.Kind,
			Title:   fmt.Sprintf(title, violation.From, violation.To),
			Detail:  existing + fmt.Sprintf("%s; %d reference(s), %d import(s), first %s at %s:%d", violation.Rule, len(violation.References), len(violation.Imports), first, file, line),
			Targets: []string{violation.From, violation.To},
			Blast:   blast[blastTarget],
			File:    file, Line: line, Existing: violation.Existing,
			Target: EdgeTarget(violation.From, violation.To), Fingerprint: fingerprint(violation.Kind, violation.From, violation.To),
		})
	}

	for _, module := range head.Modules {
		var before *graph.Module
		if base != nil {
			before = base.Module(module.ID)
		}

		if len(module.Ambiguous) > 0 {
			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingAmbiguous,
				Title:   fmt.Sprintf("%s is claimed by boundaries %s", module.ID, strings.Join(module.Ambiguous, " and ")),
				Detail:  "Their globs match it equally well, so it is in no boundary and its dependencies are not checked. Name its path exactly in the boundary it belongs to.",
				Targets: []string{module.ID},
				File:    firstFile(module),
				Target:  module.ID, Fingerprint: fingerprint(FindingAmbiguous, append([]string{module.ID}, module.Ambiguous...)...),
				Existing: before != nil && slices.Equal(before.Ambiguous, module.Ambiguous),
			})
		} else if module.Layer == graph.LayerUnclassified {
			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingUnclassified,
				Title:   fmt.Sprintf("%s matches no layer", module.ID),
				Detail:  "No glob in treaty.yaml matches this module, so its dependencies are not checked.",
				Targets: []string{module.ID},
				File:    firstFile(module),
				Target:  module.ID, Fingerprint: fingerprint(FindingUnclassified, module.ID),
				Existing: before != nil && before.Layer == graph.LayerUnclassified && len(before.Ambiguous) == 0,
			})
		}

		if base != nil && base.Module(module.ID) == nil {
			detail := fmt.Sprintf("Placed in the %s layer.", module.Layer)
			if module.Boundary != "" {
				detail = fmt.Sprintf("Placed in the %s layer of boundary %s.", module.Layer, module.Boundary)
			}

			result = append(result, Finding{
				Severity: SeverityMedium, Kind: FindingNewModule,
				Title:   fmt.Sprintf("New module %s", module.ID),
				Detail:  detail,
				Targets: []string{module.ID},
				File:    firstFile(module),
				Target:  module.ID, Fingerprint: fingerprint(FindingNewModule, module.ID),
			})
		}
	}

	for _, symbol := range head.Symbols {
		details, print := disagreement(symbol)
		if len(details) == 0 {
			continue
		}

		existing := false
		if base != nil {
			if before := base.Symbol(symbol.ID); before != nil {
				_, was := disagreement(before)
				existing = was == print
			}
		}

		result = append(result, Finding{
			Severity: SeverityMedium, Kind: FindingVariants,
			Title:   fmt.Sprintf("Build variants of %s disagree", symbol.ID),
			Detail:  fmt.Sprintf("%s:%d declares %s; %s", symbol.File, symbol.Line, symbol.Signature, strings.Join(details, "; ")),
			Targets: []string{symbol.ID},
			Blast:   blast[symbol.ID],
			File:    symbol.File, Line: symbol.Line,
			Target: symbol.ID, Fingerprint: fingerprint(FindingVariants, symbol.ID, print), Existing: existing,
		})
	}

	var implementation []string
	for _, change := range changes {
		symbol := head.Symbol(change.Symbol)
		file, line := locate(head, base, change.Symbol)
		switch change.Kind {
		case ChangeBreaking, ChangeRemoved:
			finding := Finding{
				Severity: SeverityHigh, Kind: FindingBreaking,
				Title:   fmt.Sprintf("Breaking change to %s", change.Symbol),
				Detail:  describe(change),
				Targets: []string{change.Symbol},
				Blast:   blast[change.Symbol],
				File:    file, Line: line,
			}

			if symbol != nil && symbol.Kind == graph.KindInterface && change.Before == "" {
				finding.Severity, finding.Kind = SeverityMedium, FindingImplementers
				finding.Title = fmt.Sprintf("Interface %s gained methods", change.Symbol)
				finding.Detail = "Existing implementers no longer satisfy it."
			}

			if change.Absorbed {
				finding.Severity = SeverityMedium
				finding.Title += ", absorbed in this repository"
				finding.Detail += "; only this repository can use it, and every dependent changed with it"
			}

			finding.Target, finding.Fingerprint = change.Symbol, changePrint(finding.Kind, change)
			result = append(result, finding)
		case ChangeAdded:
			if symbol != nil && symbol.Contract {
				result = append(result, Finding{
					Severity: SeverityMedium, Kind: FindingNewContract,
					Title:   fmt.Sprintf("New contract %s", change.Symbol),
					Detail:  symbol.Signature,
					Targets: []string{change.Symbol},
					Blast:   blast[change.Symbol],
					File:    file, Line: line,
					Target: change.Symbol, Fingerprint: changePrint(FindingNewContract, change),
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
				File:    file, Line: line,
				Target: change.Symbol, Fingerprint: changePrint(FindingMoved, change),
			})
		case ChangeContract:
			result = append(result, Finding{
				Severity: SeverityLow, Kind: FindingCompatible,
				Title:   fmt.Sprintf("Compatible change to %s", change.Symbol),
				Detail:  describe(change),
				Targets: []string{change.Symbol},
				Blast:   blast[change.Symbol],
				File:    file, Line: line,
				Target: change.Symbol, Fingerprint: changePrint(FindingCompatible, change),
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

// locate finds where a changed symbol is: its line in head, or, for a
// symbol head no longer has, just the file the base had it in, since the
// base's line numbers do not hold in head.
func locate(head, base *graph.Graph, id string) (file string, line int) {
	if symbol := head.Symbol(id); symbol != nil {
		return symbol.File, symbol.Line
	}

	if base != nil {
		if symbol := base.Symbol(id); symbol != nil {
			return symbol.File, 0
		}
	}

	return "", 0
}

// firstFile names a module's first file, where a finding about the whole
// module shows.
func firstFile(module *graph.Module) string {
	if len(module.Files) == 0 {
		return ""
	}

	return slices.Min(module.Files)
}

// disagreement describes each build variant of a symbol that declares it
// otherwise than its first declaration, and sums up every declaration's
// signature and fields, without their files and lines, so the sum changes
// only when a declaration does.
func disagreement(symbol *graph.Symbol) (details []string, print string) {
	declared := []string{symbol.Signature + fieldsText(symbol.Fields)}
	for _, variant := range symbol.Variants {
		if variant.Signature == "" || (variant.Signature == symbol.Signature && fieldsEqual(variant.Fields, symbol.Fields)) {
			continue
		}

		details = append(details, fmt.Sprintf("%s:%d declares %s", variant.File, variant.Line, variant.Signature))
		declared = append(declared, variant.Signature+fieldsText(variant.Fields))
	}

	sort.Strings(declared[1:])
	return details, strings.Join(declared, "\x00")
}

// changePrint sums up a change as a finding of kind sees it: where the
// symbol was and is, its signatures before and after, and the fields that
// differ.
func changePrint(kind string, change Change) string {
	return fingerprint(kind, change.Symbol, change.From, change.Before, change.After, strings.Join(change.FieldsRemoved, "\n"), strings.Join(change.FieldsAdded, "\n"), fmt.Sprint(change.Absorbed))
}

func fieldsText(fields []graph.Field) string {
	var builder strings.Builder
	for _, field := range fields {
		builder.WriteString("\n" + field.Text)
	}

	return builder.String()
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
