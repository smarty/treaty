package app

import (
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

func TestReportSummary(t *testing.T) {
	report := Report{
		Failures:   []string{"1 layer violation(s)", "3 breaking change(s): 1 changed or removed, 2 moved"},
		Violations: []rules.Violation{{From: "go:store", To: "go:web", Rule: "data may not depend on presentation", Imports: []graph.Import{{From: "go:store", To: "go:web", File: "store/s.go", Line: 3}}}},
		Findings: []rules.Finding{
			{Kind: rules.FindingLayerViolation, Title: "go:store → go:web breaks a layer rule"},
			{Kind: rules.FindingBreaking, Title: "Breaking change to go:store:Load", Detail: "func Load() int → func Load() string"},
			{Kind: rules.FindingMoved, Title: "moved"},
			{Kind: rules.FindingMoved, Title: "moved again"},
			{Kind: rules.FindingImplementation, Targets: []string{"a", "b", "c"}},
		},
	}

	summary := report.Summary()
	for _, want := range []string{
		"FAIL: 1 layer violation(s); 3 breaking change(s): 1 changed or removed, 2 moved\n",
		"violations (1):\n  go:store → go:web: data may not depend on presentation; first an import of go:web with no references at store/s.go:3\n",
		"breaking (3):\n  Breaking change to go:store:Load: func Load() int → func Load() string\n  2 moved contract(s), each breaking its importers outside this diff; check --format text lists them\n",
		"other findings: 3 implementation\n",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}

	if strings.Count(summary, "breaks a layer rule") != 0 {
		t.Errorf("a violation is listed once, not again as a finding:\n%s", summary)
	}
}
