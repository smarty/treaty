package golang

import (
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

// holesConfig is a snowflake with one star: contracts, an assembler and an
// engine, and every other package a leaf.
const holesConfig = "version: 1\narchitecture: snowflake\nstars:\n  .:\n    contracts: [\"internal/contracts\"]\n    assemblers: [\"cmd/app\"]\n    engines: [\"internal/run\"]\n"

// holesTree is the clean tree holesConfig fits.
func holesTree(more map[string]string) map[string]string {
	result := map[string]string{
		"go.mod":                          "module example.com/m\n",
		"treaty.yaml":                     holesConfig,
		"cmd/app/main.go":                 "package main\n\nimport (\n\t\"example.com/m/internal/plan\"\n\t\"example.com/m/internal/web\"\n)\n\nfunc main() { plan.Run(web.Fetcher{}) }\n",
		"internal/contracts/contracts.go": "package contracts\n\nimport \"errors\"\n\n// Fetcher fetches a page.\ntype Fetcher interface{ Fetch() string }\n\n// ErrGone is a page that is gone.\nvar ErrGone = errors.New(\"gone\")\n\nvar _ = ErrGone\n",
		"internal/run/run.go":             "package run\n\nimport \"example.com/m/internal/contracts\"\n\n// Fetch fetches through a fetcher.\nfunc Fetch(fetcher contracts.Fetcher) string { return fetcher.Fetch() }\n",
		"internal/plan/plan.go":           "package plan\n\nimport (\n\t\"example.com/m/internal/contracts\"\n\t\"example.com/m/internal/run\"\n)\n\n// Run runs a plan.\nfunc Run(fetcher contracts.Fetcher) { run.Fetch(fetcher) }\n\n// Name names a plan.\nfunc Name() string { return \"\" }\n",
		"internal/web/web.go":             "package web\n\n// Fetcher fetches pages.\ntype Fetcher struct{}\n\n// Fetch fetches a page.\nfunc (Fetcher) Fetch() string { return \"\" }\n",
	}

	for name, text := range more {
		result[name] = text
	}

	return result
}

// holesCheck checks a tree with the holes config.
func holesCheck(t *testing.T, files map[string]string) app.Report {
	t.Helper()
	report, err := newSnowflakeRepo(t, holesTree(files)).service().Check("")
	if err != nil {
		t.Fatal(err)
	}

	return report
}

// findingsOf lists a report's findings of a kind as title: detail.
func findingsOf(report app.Report, kind string) (result []string) {
	for _, finding := range report.Findings {
		if finding.Kind == kind {
			result = append(result, finding.Title+": "+finding.Detail)
		}
	}

	return result
}

func TestHolesCleanTreePasses(t *testing.T) {
	report := holesCheck(t, nil)
	if len(report.Failures) != 0 || len(report.Violations) != 0 || len(findingsOf(report, rules.FindingImpure)) != 0 {
		t.Fatalf("a clean tree: failures %v, violations %+v, findings %+v", report.Failures, report.Violations, report.Findings)
	}
}

func TestHolesTestFileImports(t *testing.T) {
	// Failing: an internal test of web, and an external one, reach the
	// sibling plan.
	for name, text := range map[string]string{
		"internal/web/web_test.go":      "package web\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/internal/plan\"\n)\n\nfunc TestTitle(t *testing.T) { _ = plan.Name() }\n",
		"internal/web/external_test.go": "package web_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/internal/plan\"\n\t\"example.com/m/internal/web\"\n)\n\nfunc TestFetch(t *testing.T) { _ = plan.Name(); _ = web.Fetcher{} }\n",
	} {
		report := holesCheck(t, map[string]string{name: text})
		if len(report.Violations) != 1 || !report.Violations[0].Test || report.Violations[0].From != "go:internal/web" || report.Violations[0].To != "go:internal/plan" ||
			!strings.HasPrefix(report.Violations[0].Rule, "in a test: leaf internal/web may not depend on leaf internal/plan: internal/web and internal/plan are sibling spokes of star ., and no dependency runs sideways") {
			t.Fatalf("%s: violations %+v", name, report.Violations)
		}

		if !slices.Equal(report.Failures, []string{"1 layer violation(s)"}) {
			t.Errorf("%s: failures %v", name, report.Failures)
		}

		var finding rules.Finding
		for _, each := range report.Findings {
			if each.Kind == rules.FindingLayerViolation {
				finding = each
			}
		}

		if finding.Title != "go:internal/web → go:internal/plan breaks a layer rule in a test" || finding.Target != "go:internal/web -> go:internal/plan (test)" {
			t.Errorf("%s: finding %+v", name, finding)
		}
	}

	// Passing: a test of web that uses its own star's contracts and engine.
	if report := holesCheck(t, map[string]string{"internal/web/web_test.go": "package web_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/internal/contracts\"\n\t\"example.com/m/internal/run\"\n\t\"example.com/m/internal/web\"\n)\n\nfunc TestFetch(t *testing.T) { var f contracts.Fetcher = web.Fetcher{}; _ = run.Fetch(f) }\n"}); len(report.Violations) != 0 {
		t.Fatalf("a test within the rules: %+v", report.Violations)
	}

	// Passing: rules.tests: skip leaves tests unchecked.
	skip := map[string]string{
		"internal/web/web_test.go": "package web\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/internal/plan\"\n)\n\nfunc TestTitle(t *testing.T) { _ = plan.Name() }\n",
		"treaty.yaml":              holesConfig + "rules:\n  tests: skip\n",
	}

	if report := holesCheck(t, skip); len(report.Violations) != 0 {
		t.Fatalf("tests: skip: %+v", report.Violations)
	}
}

func TestHolesHiddenPackages(t *testing.T) {
	// Failing: a leaf imports a package treaty does not read, in a
	// directory starting with _, and a test imports one in testdata.
	report := holesCheck(t, map[string]string{
		"internal/plan/hidden.go":            "package plan\n\nimport \"example.com/m/internal/_shadow\"\n\n// Shadow reaches the hidden package.\nfunc Shadow() string { return shadow.Web() }\n",
		"internal/_shadow/shadow.go":         "package shadow\n\nimport \"example.com/m/internal/web\"\n\n// Web reaches a sibling unseen.\nfunc Web() string { return web.Fetcher{}.Fetch() }\n",
		"internal/web/web_test.go":           "package web\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/internal/web/testdata/fake\"\n)\n\nfunc TestFake(t *testing.T) { _ = fake.X }\n",
		"internal/web/testdata/fake/fake.go": "package fake\n\n// X is a fake.\nconst X = 1\n",
	})

	found := findingsOf(report, rules.FindingUnclassified)
	for _, want := range []string{
		"go:internal/_shadow, imported by go:internal/plan, is unclassified: go:internal/plan imports go:internal/_shadow, which lies where treaty reads no code",
		"go:internal/web/testdata/fake, imported by a test of go:internal/web, is unclassified: a test of go:internal/web imports go:internal/web/testdata/fake",
	} {
		if !slices.ContainsFunc(found, func(text string) bool { return strings.HasPrefix(text, want) }) {
			t.Errorf("no finding %q in %q", want, found)
		}
	}

	if !slices.Contains(report.Warnings, "2 unclassified module(s)") {
		t.Errorf("the unclassified rule catches them: %v", report.Warnings)
	}

	// Passing: a hidden directory nothing imports is no finding.
	if report = holesCheck(t, map[string]string{"internal/_scratch/x.go": "package scratch\n"}); len(findingsOf(report, rules.FindingUnclassified)) != 0 {
		t.Errorf("an unimported hidden directory: %q", findingsOf(report, rules.FindingUnclassified))
	}
}

func TestHolesBuildConstraints(t *testing.T) {
	// Failing: a file built only under a tag no build sets is still read.
	report := holesCheck(t, map[string]string{"internal/plan/odd.go": "//go:build linux && !linux\n\npackage plan\n\nimport \"example.com/m/internal/web\"\n\n// Odd reaches a sibling.\nfunc Odd() string { return web.Fetcher{}.Fetch() }\n"})
	if len(report.Violations) != 1 || report.Violations[0].To != "go:internal/web" {
		t.Fatalf("a constrained file: %+v", report.Violations)
	}

	// Passing: one under //go:build ignore is left out.
	report = holesCheck(t, map[string]string{"internal/plan/gen.go": "//go:build ignore\n\npackage main\n\nimport \"example.com/m/internal/web\"\n\nfunc main() { _ = web.Fetcher{} }\n"})
	if len(report.Violations) != 0 {
		t.Fatalf("an ignored file: %+v", report.Violations)
	}
}

func TestHolesLinkname(t *testing.T) {
	// Failing: a linkname directive, in code or in a test, is a violation
	// naming its target.
	report := holesCheck(t, map[string]string{
		"internal/plan/link.go":     "package plan\n\nimport _ \"unsafe\"\n\n//go:linkname nanotime runtime.nanotime\nfunc nanotime() int64\n",
		"internal/web/link_test.go": "package web\n\nimport _ \"unsafe\"\n\n//go:linkname name example.com/m/internal/plan.Name\nfunc name() string\n",
	})

	var got []string
	for _, violation := range report.Violations {
		got = append(got, violation.From+" "+violation.To+" "+violation.Rule)
	}

	want := []string{
		"go:internal/plan linkname runtime.nanotime //go:linkname nanotime runtime.nanotime links to runtime.nanotime past every import, so no rule sees what it uses; import the package and use what it exports, or declare a port",
		"go:internal/web linkname example.com/m/internal/plan.Name //go:linkname name example.com/m/internal/plan.Name in a test links to example.com/m/internal/plan.Name past every import, so no rule sees what it uses; import the package and use what it exports, or declare a port",
	}

	if !slices.Equal(got, want) || !slices.Equal(report.Failures, []string{"2 layer violation(s)"}) {
		t.Fatalf("linknames: %q, failures %v", got, report.Failures)
	}

	if !slices.ContainsFunc(report.Findings, func(finding rules.Finding) bool { return finding.Title == "go:internal/plan links to runtime.nanotime" }) {
		t.Errorf("no linkname finding: %+v", report.Findings)
	}

	// Passing: a comment that only mentions the directive is none.
	if report = holesCheck(t, map[string]string{"internal/plan/doc.go": "package plan\n\n// Never write //go:linkname here.\n"}); len(report.Violations) != 0 {
		t.Fatalf("a mention: %+v", report.Violations)
	}
}

func TestHolesImpureContracts(t *testing.T) {
	// Failing: contracts importing the network, a third-party module, cgo
	// and unsafe, and declaring a variable that is not a sentinel error.
	report := holesCheck(t, map[string]string{
		"internal/contracts/impure.go": "package contracts\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"unsafe\"\n\n\t\"github.com/acme/thing\"\n)\n\n// Client is shared state.\nvar Client = http.DefaultClient\n\n// ErrOdd is no new error.\nvar ErrOdd = fmt.Sprint(thing.X, unsafe.Sizeof(0))\n",
	})

	found := findingsOf(report, rules.FindingImpure)
	for _, want := range []string{
		"Contracts go:internal/contracts import net/http: contracts may import only the standard library, and never net/http",
		"Contracts go:internal/contracts import unsafe: contracts may import only the standard library, and never unsafe",
		"Contracts go:internal/contracts import github.com/acme/thing: contracts may import only the standard library, never a third-party module such as github.com/acme/thing",
		"Contracts go:internal/contracts declare package-level variable Client:",
		"Contracts go:internal/contracts declare package-level variable ErrOdd:",
	} {
		if !slices.ContainsFunc(found, func(text string) bool { return strings.HasPrefix(text, want) }) {
			t.Errorf("no finding %q in %q", want, found)
		}
	}

	if len(found) != 5 || !slices.Contains(report.Failures, "5 impurity(ies) in contracts") {
		t.Errorf("impure_contracts fails a snowflake by default: %q, failures %v", found, report.Failures)
	}

	// Passing: the standard library, the blank identifier and sentinel
	// errors, by errors.New or fmt.Errorf.
	report = holesCheck(t, map[string]string{
		"internal/contracts/pure.go": "package contracts\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n\t\"time\"\n)\n\n// ErrLate is a late page.\nvar ErrLate = fmt.Errorf(\"late after %s\", time.Second)\n\nvar _ = strings.ToUpper\n\n// Limit is a constant.\nconst Limit = 3\n",
	})

	if found = findingsOf(report, rules.FindingImpure); len(found) != 0 {
		t.Errorf("pure contracts: %q", found)
	}

	// An exception goes through rules.ignore like any finding.
	repo := newSnowflakeRepo(t, holesTree(map[string]string{"internal/contracts/impure.go": "package contracts\n\n// Registry is shared on purpose.\nvar Registry = map[string]int{}\n"}))
	service := repo.service()
	first, err := service.Check("")
	if err != nil {
		t.Fatal(err)
	}

	var finding rules.Finding
	for _, each := range first.Findings {
		if each.Kind == rules.FindingImpure {
			finding = each
		}
	}

	if finding.Target != "go:internal/contracts: var Registry" {
		t.Fatalf("the finding's target: %+v", finding)
	}

	repo.write(map[string]string{"treaty.yaml": holesConfig + "rules:\n  ignore:\n    - finding: impure_contracts\n      target: \"" + finding.Target + "\"\n      fingerprint: \"" + finding.Fingerprint + "\"\n      reason: the registry is read only after start\n"})
	if report, err = service.Check(""); err != nil || len(report.Failures) != 0 {
		t.Fatalf("an ignored impurity: %v, failures %v", err, report.Failures)
	}
}

func TestHolesAssemblerOnlyAccess(t *testing.T) {
	// Failing: a leaf reaches the assembler, which wires it.
	report := holesCheck(t, map[string]string{"internal/web/app.go": "package web\n\nimport _ \"example.com/m/cmd/app\"\n"})
	if len(report.Violations) != 1 || !strings.Contains(report.Violations[0].Rule, "a spoke never uses an assembler") {
		t.Fatalf("a leaf on the assembler: %+v", report.Violations)
	}

	// Passing: the assembler wires every leaf and the engine.
	if report = holesCheck(t, map[string]string{"cmd/app/run.go": "package main\n\nimport _ \"example.com/m/internal/run\"\n"}); len(report.Violations) != 0 {
		t.Fatalf("the assembler wires: %+v", report.Violations)
	}
}

func TestHolesNestedUnderCore(t *testing.T) {
	// Failing: a package beneath the engine that no entry names is
	// unclassified, and noted.
	nested := map[string]string{"internal/run/inner/inner.go": "package inner\n\n// X is inner.\nconst X = 1\n"}
	report := holesCheck(t, nested)
	if !slices.Contains(report.Warnings, "1 unclassified module(s)") || !slices.ContainsFunc(report.Notes, func(note string) bool {
		return strings.HasPrefix(note, "internal/run/inner lies beneath engine internal/run of star .")
	}) {
		t.Fatalf("nested: failures %v, notes %v", report.Failures, report.Notes)
	}

	// Passing: listed, it has its own role.
	nested["treaty.yaml"] = strings.Replace(holesConfig, `engines: ["internal/run"]`, `engines: ["internal/run", "internal/run/inner"]`, 1)
	if report = holesCheck(t, nested); len(report.Failures)+len(report.Warnings) != 0 {
		t.Fatalf("listed: failures %v, warnings %v", report.Failures, report.Warnings)
	}
}

func TestHolesDeprecatedOrchestrators(t *testing.T) {
	// Passing, with a note: orchestrators is read as assemblers.
	config := strings.Replace(holesConfig, "assemblers:", "orchestrators:", 1)
	report := holesCheck(t, map[string]string{"treaty.yaml": config})
	if len(report.Failures) != 0 || !slices.ContainsFunc(report.Notes, func(note string) bool {
		return strings.HasPrefix(note, "star . lists orchestrators, the name the second snowflake gave its assemblers")
	}) {
		t.Fatalf("orchestrators: failures %v, notes %v", report.Failures, report.Notes)
	}

	// Failing: a leaf on an assembler listed under orchestrators breaks the
	// same rule.
	report = holesCheck(t, map[string]string{"treaty.yaml": config, "internal/web/app.go": "package web\n\nimport _ \"example.com/m/cmd/app\"\n"})
	if len(report.Violations) != 1 || !strings.Contains(report.Violations[0].Rule, "a spoke never uses an assembler") {
		t.Fatalf("a leaf on an orchestrator: %+v", report.Violations)
	}
}
