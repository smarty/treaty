package app

import (
	"github.com/smarty/treaty/internal/rules"
)

const ReportSchema = "treaty/report/v1"

// ModuleReport is one module's layer and metrics.
type ModuleReport struct {
	ID      string        `json:"id"`
	Layer   string        `json:"layer"`
	Side    string        `json:"side,omitempty"`
	Before  *rules.Metric `json:"before,omitempty"`
	Metrics rules.Metric  `json:"metrics"`
}

// Report is the output of treaty check.
type Report struct {
	Schema     string            `json:"schema"`
	Base       string            `json:"base,omitempty"`
	Modules    []ModuleReport    `json:"modules"`
	Changes    []rules.Change    `json:"changes,omitempty"`
	Violations []rules.Violation `json:"violations"`
	Findings   []rules.Finding   `json:"findings"`
	Strength   string            `json:"strength"`
	Failures   []string          `json:"failures"`
}

// Check runs every check on the working tree, and on the diff from base when
// base is not empty.
//
// Notes:
//   - Test strength is not computed yet; the report says so.
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

	return analysis.report(), nil
}

func (this *analysis) report() Report {
	result := Report{
		Schema:     ReportSchema,
		Base:       this.baseRef,
		Changes:    this.changes,
		Violations: this.violations,
		Findings:   this.findings,
		Strength:   "not computed: mutation testing arrives in phase 3",
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
		entry := ModuleReport{ID: module.ID, Layer: module.Layer, Side: module.Side, Metrics: this.metrics[module.ID]}
		if before, ok := this.baseMetrics[module.ID]; ok {
			entry.Before = &before
		}

		result.Modules = append(result.Modules, entry)
	}

	return result
}
