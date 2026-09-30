package app

import (
	"fmt"

	"github.com/smarty/treaty/internal/rules"
)

const ReportSchema = "treaty/report/v1"

// ModuleReport is one module's placement and metrics. Section is the
// vertical slice or bounded context holding it.
type ModuleReport struct {
	ID      string        `json:"id"`
	Layer   string        `json:"layer"`
	Side    string        `json:"side,omitempty"`
	Section string        `json:"section,omitempty"`
	Public  bool          `json:"public,omitempty"`
	Before  *rules.Metric `json:"before,omitempty"`
	Metrics rules.Metric  `json:"metrics"`
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

	result = analysis.report()
	result.Notes = this.notes(base, analysis)
	return result, nil
}

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
		Findings:   this.findings,
		Failures:   this.failures(),
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
		entry := ModuleReport{ID: module.ID, Layer: module.Layer, Side: module.Side, Section: module.Slice, Public: module.Public, Metrics: this.metrics[module.ID]}
		if before, ok := this.baseMetrics[module.ID]; ok {
			entry.Before = &before
		}

		result.Modules = append(result.Modules, entry)
	}

	return result
}
