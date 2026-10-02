package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/graph"
)

// fakeSuite finds two tests in go:core and passes one, fails the other,
// covering line 3 and missing line 4 of core/core.go.
type fakeSuite struct {
	mutex    sync.Mutex
	requests [][]TestRequest
	release  chan struct{}
}

// fakeSources serves files from memory.
type fakeSources map[string]string

func (this *fakeSuite) Discover(string, *graph.Graph) ([]TestCase, error) {
	return []TestCase{
		{Module: "go:core", Name: "TestB", File: "core/core_test.go", Line: 9},
		{Module: "go:core", Name: "TestA", File: "core/core_test.go", Line: 3, Targets: []string{"go:core:A"}},
	}, nil
}

func (this *fakeSuite) Language() string {
	return "go"
}

func (this *fakeSuite) Run(ctx context.Context, _ string, requests []TestRequest, report func(TestOutcome)) (map[string]LineCoverage, error) {
	this.mutex.Lock()
	this.requests = append(this.requests, requests)
	this.mutex.Unlock()
	if this.release != nil {
		select {
		case <-this.release:
		case <-ctx.Done():
			return nil, nil
		}
	}

	report(TestOutcome{Module: "go:core", Name: "TestA", Status: TestRunning})
	report(TestOutcome{Module: "go:core", Name: "TestA", Status: TestPassed, Elapsed: 0.1})
	report(TestOutcome{Module: "go:core", Name: "TestA/sub", Status: TestPassed})
	if len(requests[0].Names) != 1 {
		report(TestOutcome{Module: "go:core", Name: "TestB", Status: TestFailed, Output: "boom"})
	}

	return map[string]LineCoverage{"core/core.go": {Covered: []int{3}, Uncovered: []int{4}}}, nil
}

func (this fakeSources) Extract(string) (*graph.Graph, error) {
	return graph.New(), nil
}

func (this fakeSources) Source(_, file string) ([]byte, error) {
	text, ok := this[file]
	if !ok {
		return nil, os.ErrNotExist
	}

	return []byte(text), nil
}

// settle waits for the bench to finish its run.
func settle(t *testing.T, tests *Tests, g *graph.Graph) TestReport {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if report := tests.Report(g); !report.Running {
			return report
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("the run did not finish")
	return TestReport{}
}

func TestTestsRunRecordsOutcomesAndCoverage(t *testing.T) {
	suite, sources, g := &fakeSuite{}, fakeSources{"core/core.go": "v1"}, graph.New()
	tests := NewTests("/repo", sources, []TestSuite{suite})
	report := tests.Report(g)
	if len(report.Tests) != 2 || report.Tests[0].ID != "go:core#TestA" || len(report.Results) != 0 || len(report.Coverage) != 0 {
		t.Fatalf("before running: %+v", report)
	}

	if err := tests.Run(g, []string{"go:core#TestA", "go:core#TestB"}); err != nil {
		t.Fatal(err)
	}

	report = settle(t, tests, g)
	if got := suite.requests[0]; len(got) != 1 || got[0].Module != "go:core" || got[0].Names != nil {
		t.Errorf("every test of a module runs it whole: %+v", got)
	}

	if report.Results["go:core#TestA"].Status != TestPassed || report.Results["go:core#TestB"].Output != "boom" || report.Results["go:core#TestA/sub"].Status != TestPassed {
		t.Errorf("results: %+v", report.Results)
	}

	if lines := report.Coverage["core/core.go"]; !slices.Equal(lines.Covered, []int{3}) || !slices.Equal(lines.Uncovered, []int{4}) {
		t.Errorf("coverage: %+v", report.Coverage)
	}

	if err := tests.Run(g, []string{"go:core#TestA/sub"}); err != nil {
		t.Fatalf("a subtest that has run can run again: %v", err)
	}

	settle(t, tests, g)
	if got := suite.requests[1]; got[0].Names[0] != "TestA/sub" {
		t.Errorf("subtest request: %+v", got)
	}

	sources["core/core.go"] = "v2"
	if report := tests.Report(g); len(report.Coverage) != 0 {
		t.Errorf("coverage of a changed file is out of date: %+v", report.Coverage)
	}
}

func TestTestsRunRefusesUnknownTestsAndASecondRun(t *testing.T) {
	suite, g := &fakeSuite{release: make(chan struct{})}, graph.New()
	tests := NewTests("/repo", fakeSources{}, []TestSuite{suite})
	if err := tests.Run(g, []string{"go:core#TestMissing"}); !errors.Is(err, ErrUnknownTest) {
		t.Fatalf("unknown: %v", err)
	}

	if err := tests.Run(g, []string{"go:core#TestA"}); err != nil {
		t.Fatal(err)
	}

	if report := tests.Report(g); !report.Running || report.Results["go:core#TestA"].Status != TestQueued {
		t.Fatalf("queued: %+v", report)
	}

	if err := tests.Run(g, []string{"go:core#TestB"}); !errors.Is(err, ErrTestsRunning) {
		t.Fatalf("second run: %v", err)
	}

	tests.Stop()
	if report := settle(t, tests, g); len(report.Results) != 0 {
		t.Fatalf("a stopped run forgets what never ran: %+v", report.Results)
	}
}
