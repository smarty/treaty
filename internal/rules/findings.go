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
	FindingImpure         = "impure_contracts"
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
	FindingKinds = []string{FindingAmbiguous, FindingBreaking, FindingCompatible, FindingCycle, FindingImplementation, FindingImplementers, FindingImpure, FindingLayerViolation, FindingMoved, FindingNewContract, FindingNewModule, FindingUnclassified, FindingVariants}

	// Failable lists the kinds of finding that can fail a check, or warn.
	Failable = []string{FindingBreaking, FindingLayerViolation, FindingCycle, FindingUnclassified, FindingAmbiguous, FindingImpure}
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

		title, to := "%s → %s breaks a layer rule", violation.To
		switch {
		case violation.Kind == FindingCycle:
			title = "%s → %s closes a cycle"
		case violation.ToLayer == "linkname":
			title, to = "%s links to %s", strings.TrimPrefix(violation.To, "linkname ")
		case violation.Test:
			title = "%s → %s breaks a layer rule in a test"
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
			Title:   fmt.Sprintf(title, violation.From, to),
			Detail:  existing + fmt.Sprintf("%s; %d reference(s), %d import(s), first %s at %s:%d", violation.Rule, len(violation.References), len(violation.Imports), first, file, line),
			Targets: []string{violation.From, violation.To},
			Blast:   blast[blastTarget],
			File:    file, Line: line, Existing: violation.Existing,
			Target: violation.Target(), Fingerprint: fingerprint(violation.Kind, violation.From, violation.To+testMark(violation.Test)),
		})
	}

	result = append(result, unread(head, base)...)
	result = append(result, impure(head, base)...)

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
				Title:   fmt.Sprintf("%s is unclassified", module.ID),
				Detail:  "treaty.yaml places this module nowhere, so its dependencies are not checked.",
				Targets: []string{module.ID},
				File:    firstFile(module),
				Target:  module.ID, Fingerprint: fingerprint(FindingUnclassified, module.ID),
				Existing: before != nil && before.Layer == graph.LayerUnclassified && len(before.Ambiguous) == 0,
			})
		}

		if base != nil && base.Module(module.ID) == nil {
			detail := fmt.Sprintf("Placed in the %s layer.", module.Layer)
			switch {
			case module.Layer == graph.LayerLeaf:
				detail = fmt.Sprintf("Placed in leaf %s of star %s.", module.Spoke, module.Star)
			case module.Layer == graph.LayerContracts, module.Layer == graph.LayerAssembler, module.Layer == graph.LayerEngine:
				detail = fmt.Sprintf("Placed in %s %s of star %s.", module.Layer, module.CoreEntry, module.Star)
			case module.Boundary != "":
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

// unread reports each package of the repository that code imports but the
// extractor did not read, such as one in a directory starting with _ or .,
// vendor or testdata, as unclassified, by the package that imports it. The
// language builds it all the same, so its code and dependencies would go
// unchecked.
func unread(head, base *graph.Graph) (result []Finding) {
	seen := map[[2]string]bool{}
	before := map[[2]string]bool{}
	if base != nil {
		for _, item := range base.Unread {
			before[[2]string{item.From, item.To}] = true
		}
	}

	for _, item := range head.Unread {
		key := [2]string{item.From, item.To}
		if seen[key] {
			continue
		}

		seen[key] = true
		importer := item.From
		if item.Test {
			importer = "a test of " + item.From
		}

		result = append(result, Finding{
			Severity: SeverityMedium, Kind: FindingUnclassified,
			Title:   fmt.Sprintf("%s, imported by %s, is unclassified", item.To, importer),
			Detail:  fmt.Sprintf("%s imports %s, which lies where treaty reads no code, such as a directory starting with _ or ., vendor or testdata; the package is built all the same, so neither it nor what it uses is checked. Move it to a directory treaty reads.", importer, item.To),
			Targets: []string{item.From, item.To},
			File:    item.File, Line: item.Line,
			Target: item.To + " imported by " + item.From, Fingerprint: fingerprint(FindingUnclassified, item.To, item.From),
			Existing: before[key],
		})
	}

	return result
}

// impureImports are the standard packages contracts may not import, and
// those under them: the network, processes and signals, the system's
// calls, unsafe memory, plugins and cgo.
var impureImports = []string{"net", "os/exec", "os/signal", "syscall", "unsafe", "plugin", "C"}

// impure reports what makes a snowflake's contracts impure: an import of
// anything but the standard library, or of a standard package that reaches
// the world outside the process or past the type system, and a package-level
// variable that is not a sentinel error. Each is its own finding, its target
// the module and what it holds, so treaty.yaml can ignore it alone.
func impure(head, base *graph.Graph) (result []Finding) {
	contracts := func(g *graph.Graph, id string) bool {
		module := g.Module(id)
		return module != nil && module.Layer == graph.LayerContracts
	}

	before := map[string]bool{}
	if base != nil {
		for _, item := range base.Foreign {
			if contracts(base, item.From) {
				before[item.From+": import "+item.Path] = true
			}
		}

		for _, global := range base.Globals {
			if contracts(base, global.Module) {
				before[global.Module+": var "+global.Name] = true
			}
		}
	}

	seen := map[string]bool{}
	add := func(target, title, detail, file string, line int) {
		if seen[target] {
			return
		}

		seen[target] = true
		module, _, _ := strings.Cut(target, ": ")
		result = append(result, Finding{
			Severity: SeverityHigh, Kind: FindingImpure,
			Title: title, Detail: detail,
			Targets: []string{module},
			File:    file, Line: line,
			Target: target, Fingerprint: fingerprint(FindingImpure, target),
			Existing: before[target],
		})
	}

	for _, item := range head.Foreign {
		if !contracts(head, item.From) {
			continue
		}

		target := item.From + ": import " + item.Path
		switch {
		case slices.ContainsFunc(impureImports, func(path string) bool { return item.Path == path || strings.HasPrefix(item.Path, path+"/") }):
			add(target, fmt.Sprintf("Contracts %s import %s", item.From, item.Path),
				fmt.Sprintf("contracts may import only the standard library, and never %s, which reaches the network, processes, signals, system calls, unsafe memory, plugins or cgo; move what uses it to a spoke behind a port", item.Path), item.File, item.Line)
		case !item.Standard:
			add(target, fmt.Sprintf("Contracts %s import %s", item.From, item.Path),
				fmt.Sprintf("contracts may import only the standard library, never a third-party module such as %s; declare the types they need themselves, and let a spoke adapt the module", item.Path), item.File, item.Line)
		}
	}

	for _, global := range head.Globals {
		if global.Name == "_" || global.Sentinel || !contracts(head, global.Module) {
			continue
		}

		add(global.Module+": var "+global.Name, fmt.Sprintf("Contracts %s declare package-level variable %s", global.Module, global.Name),
			fmt.Sprintf("contracts declare no package-level variable but sentinel errors, named Err... and made by errors.New or fmt.Errorf; make %s a constant, a function or a field of a value an assembler builds", global.Name), global.File, global.Line)
	}

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
