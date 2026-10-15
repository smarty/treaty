package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/rules"
)

const ReportSchema = "treaty/report/v1"

// ModuleReport is one module's placement and metrics. Boundary is the
// boundary holding it, for the vertical and modular architectures.
type ModuleReport struct {
	ID       string        `json:"id"`
	Layer    string        `json:"layer"`
	Side     string        `json:"side,omitempty"`
	Boundary string        `json:"boundary,omitempty"`
	Public   bool          `json:"public,omitempty"`
	Before   *rules.Metric `json:"before,omitempty"`
	Metrics  rules.Metric  `json:"metrics"`
}

// Report is the output of treaty check. Notes say what the check could not
// compare, so an empty review queue is not mistaken for a clean diff.
type Report struct {
	Schema     string            `json:"schema"`
	Base       string            `json:"base,omitempty"`
	Notes      []string          `json:"notes,omitempty"`
	Modules    []ModuleReport    `json:"modules"`
	Changes    []rules.Change    `json:"changes,omitempty"`
	Violations []rules.Violation `json:"violations"`
	Findings   []rules.Finding   `json:"findings"`
	Failures   []string          `json:"failures"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// Check runs every check on the working tree, and on the diff from base when
// base is not empty.
//
// Parameters:
//   - base: the git ref to diff against, or empty for no diff.
//
// Returns:
//   - result: the report; result.Failures is non-empty when the check fails.
//   - err: the tree, config or base could not be read.
func (this *Service) Check(base string) (result Report, err error) {
	analysis, err := this.analyze(base)
	if err != nil {
		return Report{}, err
	}

	return this.checkReport(base, analysis), nil
}

// checkReport turns an analysis against base into the check's report.
func (this *Service) checkReport(base string, analysis *analysis) (result Report) {
	result = analysis.report()
	result.Notes = append(this.notes(base, analysis), result.Notes...)
	if analysis.config.Unversioned {
		result.Notes = append(result.Notes, unversionedNote)
	}

	return result
}

// unversionedNote asks for a version line in a treaty.yaml without one.
var unversionedNote = fmt.Sprintf("treaty.yaml has no version line, so it is read as version 0, the format from before versions; add \"version: %d\" at its top", ConfigVersion)

// notes explains a check that compared nothing: one with no base, or whose
// base is the current commit while the working tree matches it.
func (this *Service) notes(base string, analysis *analysis) []string {
	if base == "" {
		return []string{"no base given, so only the architecture was checked; pass --base <ref> to classify contract changes since that ref"}
	}

	if len(analysis.changes) > 0 || this.vcs == nil {
		return nil
	}

	baseCommit, baseErr := this.vcs.Resolve(base)
	headCommit, headErr := this.vcs.Resolve("HEAD")
	if baseErr != nil || headErr != nil || baseCommit != headCommit {
		return nil
	}

	return []string{fmt.Sprintf("base %s is the current commit and the working tree matches it, so there was nothing to compare; pass the commit you started from", base)}
}

func (this *analysis) report() Report {
	result := Report{
		Schema:     ReportSchema,
		Base:       this.baseRef,
		Changes:    this.changes,
		Violations: this.violations,
		Findings:   this.judge(this.findings),
		Failures:   this.failures(),
		Warnings:   this.warnings(),
	}

	if existing := this.existing(); existing > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%d violation(s) were already in base %s, so they do not fail the check; they are listed as existing", existing, this.baseRef))
	}

	if result.Violations == nil {
		result.Violations = []rules.Violation{}
	}

	if result.Findings == nil {
		result.Findings = []rules.Finding{}
	}

	if result.Failures == nil {
		result.Failures = []string{}
	}

	for _, module := range this.head.Modules {
		entry := ModuleReport{ID: module.ID, Layer: module.Layer, Side: module.Side, Boundary: module.Boundary, Public: module.Public, Metrics: this.metrics[module.ID]}
		if before, ok := this.baseMetrics[module.ID]; ok {
			entry.Before = &before
		}

		result.Modules = append(result.Modules, entry)
	}

	return result
}

// judge marks each finding that counts toward the failures or warnings,
// counted as problems counts them: a violation or unplaced module the base
// did not have, or a breaking change, such as a moved contract unless it
// left an entry module nothing could import.
func (this *analysis) judge(findings []rules.Finding) []rules.Finding {
	verdicts := map[string]string{}
	for _, kind := range this.config.WarnOn {
		verdicts[kind] = rules.VerdictWarn
	}

	for _, kind := range this.config.FailOn {
		verdicts[kind] = rules.VerdictFail
	}

	harmless := map[string]bool{}
	for _, change := range this.changes {
		if change.Kind == rules.ChangeMoved && this.fromEntry(change) {
			harmless[change.Symbol] = true
		}
	}

	result := make([]rules.Finding, len(findings))
	for i, finding := range findings {
		kind := finding.Kind
		switch finding.Kind {
		case rules.FindingBreaking:
		case rules.FindingImplementers:
			kind = rules.FindingBreaking
		case rules.FindingMoved:
			kind = rules.FindingBreaking
			if harmless[finding.Targets[0]] {
				kind = ""
			}
		case rules.FindingLayerViolation, rules.FindingCycle:
			if finding.Existing {
				kind = ""
			}
		case rules.FindingUnclassified, rules.FindingAmbiguous:
			if this.unplacedInBase(finding.Targets[0], finding.Kind) {
				kind = ""
			}
		default:
			kind = ""
		}

		finding.Verdict = verdicts[kind]
		result[i] = finding
	}

	return result
}

// existing counts the violations the base already had.
func (this *analysis) existing() (result int) {
	for _, violation := range this.violations {
		if violation.Existing {
			result++
		}
	}

	return result
}

// Summary renders the report in a few lines, for agents checking their work:
// the verdict and notes, one line per violation and per breaking change,
// since each needs action or an explanation, a count of moved contracts, and
// counts for every other kind of finding.
//
// Returns:
//   - result: the summary text.
func (this Report) Summary() string {
	var builder strings.Builder
	if len(this.Failures) > 0 {
		fmt.Fprintf(&builder, "FAIL: %s\n", strings.Join(this.Failures, "; "))
	} else {
		builder.WriteString("PASS\n")
	}

	for _, warning := range this.Warnings {
		fmt.Fprintf(&builder, "warning: %s\n", warning)
	}

	for _, note := range this.Notes {
		fmt.Fprintf(&builder, "note: %s\n", note)
	}

	if len(this.Violations) > 0 {
		fmt.Fprintf(&builder, "violations (%d):\n", len(this.Violations))
		for _, violation := range this.Violations {
			file, line, first := violation.First()
			existing := ""
			if violation.Existing {
				existing = " (existing)"
			}

			fmt.Fprintf(&builder, "  %s → %s%s: %s; first %s at %s:%d\n", violation.From, violation.To, existing, violation.Rule, first, file, line)
		}
	}

	counts := map[string]int{}
	var breaking []rules.Finding
	moved := 0
	for _, finding := range this.Findings {
		switch finding.Kind {
		case rules.FindingBreaking, rules.FindingImplementers:
			breaking = append(breaking, finding)
		case rules.FindingMoved:
			moved++
		case rules.FindingLayerViolation, rules.FindingCycle:
		case rules.FindingImplementation:
			counts[finding.Kind] += len(finding.Targets)
		default:
			counts[finding.Kind]++
		}
	}

	// Moved contracts break their importers too. They are often many, as
	// in a refactor, so they are counted here rather than listed.
	if len(breaking)+moved > 0 {
		fmt.Fprintf(&builder, "breaking (%d):\n", len(breaking)+moved)
		for _, finding := range breaking {
			fmt.Fprintf(&builder, "  %s: %s\n", finding.Title, finding.Detail)
		}

		if moved > 0 {
			fmt.Fprintf(&builder, "  %d moved contract(s), each breaking its importers outside this diff; check --format text lists them\n", moved)
		}
	}

	if len(counts) > 0 {
		kinds := make([]string, 0, len(counts))
		for kind := range counts {
			kinds = append(kinds, kind)
		}

		sort.Strings(kinds)
		parts := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
		}

		fmt.Fprintf(&builder, "other findings: %s\n", strings.Join(parts, ", "))
	}

	return builder.String()
}
