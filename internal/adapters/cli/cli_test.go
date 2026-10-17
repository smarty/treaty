package cli

import (
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/app"
)

func TestCheckTextNamesSnowflakePlacements(t *testing.T) {
	report := app.Report{Modules: []app.ModuleReport{
		{ID: "go:internal/blocks/contracts", Layer: "contracts", Star: "internal/blocks", Entry: "internal/blocks/contracts"},
		{ID: "go:.", Layer: "orchestrator", Star: ".", Entry: "."},
		{ID: "go:internal/blocks/www/fetch", Layer: "leaf", Star: "internal/blocks", Spoke: "internal/blocks/www"},
		{ID: "go:internal/blocks", Layer: "unclassified", Star: "internal/blocks"},
		{ID: "go:internal/app", Layer: "application"},
		{ID: "go:internal/adapters/web", Layer: "adapter", Side: "driving"},
	}}

	lines := strings.Split(checkText(report), "\n")[1:7]
	for i, label := range []string{
		"contracts internal/blocks/contracts",
		"orchestrator .",
		"leaf internal/blocks/www",
		"unclassified",
		"application",
		"adapter/driving",
	} {
		// Each line is two spaces, the id in 44 and a space, then the label.
		if len(lines[i]) < 47 || !strings.HasPrefix(lines[i][47:], label+" ") {
			t.Errorf("line %d labels its module %q: %q", i, label, lines[i])
		}
	}

	// The metrics start in one column, however long the labels.
	column := strings.Index(lines[0], "Ca=")
	for _, line := range lines {
		if strings.Index(line, "Ca=") != column {
			t.Errorf("the metrics are out of line:\n%s", strings.Join(lines, "\n"))
			break
		}
	}

	if column != 2+44+1+len("contracts internal/blocks/contracts")+1 {
		t.Errorf("the label column is as wide as its longest label: metrics at %d", column)
	}
}

func TestCheckTextKeepsAMinimumLabelWidth(t *testing.T) {
	report := app.Report{Modules: []app.ModuleReport{{ID: "go:x", Layer: "domain"}}}
	if line := strings.Split(checkText(report), "\n")[1]; strings.Index(line, "Ca=") != 2+44+1+20+1 {
		t.Errorf("short labels keep a column of 20: %q", line)
	}
}
