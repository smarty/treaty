package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const (
	TestFailed  = "fail"
	TestPassed  = "pass"
	TestQueued  = "queued"
	TestRunning = "running"
	TestSkipped = "skip"

	// notifyEvery batches the changes a run makes, so a suite of a thousand
	// tests does not push a thousand states.
	notifyEvery = 200 * time.Millisecond
)

var (
	ErrNoTests      = errors.New("no test suite is configured")
	ErrTestsRunning = errors.New("tests are already running")
	ErrUnknownTest  = errors.New("unknown test")
)

// LineCoverage is which of a file's lines a test run executed. A line
// holding no statement is in neither list.
type LineCoverage struct {
	Covered   []int `json:"covered"`
	Uncovered []int `json:"uncovered"`
}

// RunCoverage is what one run measured. Lines are the lines it covered and
// missed, by file. Probes are what instrumentation counted in each file it
// instrumented; Hits are, by test id, the probes of each file the test
// reached; and Shared marks a test whose run overlapped another's. A run
// that could not be instrumented has only Lines.
type RunCoverage struct {
	Lines  map[string]LineCoverage
	Probes map[string][]rules.Probe
	Hits   map[string]map[string][]int
	Shared map[string]bool
}

// TestCase is one test a suite found. ID is its module's id and its name,
// joined by "#". Kind is test, fuzz or example. Targets are the symbols the
// test uses, directly or through helpers in its own test files.
type TestCase struct {
	ID      string   `json:"id"`
	Module  string   `json:"module"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Targets []string `json:"targets,omitempty"`
}

// TestOutcome is what happened to one test, or to a whole module when Name
// is empty. Status is queued, running, pass, fail or skip; Elapsed is in
// seconds.
type TestOutcome struct {
	Module  string  `json:"module"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Elapsed float64 `json:"elapsed,omitempty"`
	Output  string  `json:"output,omitempty"`
}

// TestReport is everything the map shows about tests: what exists, how the
// latest runs went and which lines they covered. Coverage is left out for a
// file that changed since it was covered, since its lines no longer match.
//
// Measures are what the runs reached of each module and contract, counting
// only files whose probes still match their text.
type TestReport struct {
	Version  int                     `json:"version"`
	Running  bool                    `json:"running"`
	Tests    []TestCase              `json:"tests"`
	Results  map[string]TestOutcome  `json:"results"`
	Coverage map[string]LineCoverage `json:"coverage"`
	Measures *Measures               `json:"measures,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

// Measures are what test runs reached, by module id and by contract id.
type Measures struct {
	Modules   map[string]rules.Exercised `json:"modules"`
	Contracts map[string]rules.Exercised `json:"contracts"`
}

// TestRequest asks a suite to run tests in one module: the named ones, or
// every test when Names is empty. A name may select a subtest, such as
// TestParse/empty.
type TestRequest struct {
	Module string
	Names  []string
}

// Tests finds a repository's tests, runs them one batch at a time, and keeps
// their outcomes and the coverage of every run since each file last changed.
type Tests struct {
	root    string
	sources SourceExtractor
	suites  map[string]TestSuite

	discovering sync.Mutex
	discovered  *graph.Graph
	cases       []TestCase
	failure     string

	mutex    sync.Mutex
	results  map[string]TestOutcome
	coverage map[string]coveredFile
	shared   map[string]bool
	running  bool
	cancel   context.CancelFunc
	runError string
	version  int
	changed  func()
	pending  bool
}

// coveredFile is the coverage of one file's text, identified by its hash:
// for every line holding a statement, whether some run executed it, and,
// when a run instrumented it, its probes and those each test reached.
type coveredFile struct {
	hash   string
	lines  map[int]bool
	probes []rules.Probe
	hits   map[string][]int
}

// NewTests creates the test bench for a repository.
//
// Parameters:
//   - root: the repository root.
//   - sources: reads files, to tell when coverage is out of date.
//   - suites: one suite per language whose tests can run.
//
// Returns:
//   - result: the test bench.
func NewTests(root string, sources SourceExtractor, suites []TestSuite) *Tests {
	byLanguage := map[string]TestSuite{}
	for _, suite := range suites {
		byLanguage[suite.Language()] = suite
	}

	return &Tests{root: root, sources: sources, suites: byLanguage, results: map[string]TestOutcome{}, coverage: map[string]coveredFile{}, shared: map[string]bool{}}
}

// OnChange registers a function called, at most every notifyEvery, after
// outcomes or coverage change.
//
// Parameters:
//   - changed: the function; it is never called with the bench's lock held.
func (this *Tests) OnChange(changed func()) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	this.changed = changed
}

// Report lists the tests of a graph's tree with their latest outcomes and
// the coverage that still matches the files.
//
// Parameters:
//   - g: the graph of the working tree.
//
// Returns:
//   - result: the report.
func (this *Tests) Report(g *graph.Graph) (result TestReport) {
	cases, failure := this.discover(g)
	this.mutex.Lock()
	result = TestReport{Version: this.version, Running: this.running, Tests: cases, Results: make(map[string]TestOutcome, len(this.results)), Coverage: map[string]LineCoverage{}, Error: this.runError}
	for id, outcome := range this.results {
		result.Results[id] = outcome
	}

	type snapshot struct {
		hash   string
		lines  LineCoverage
		probes []rules.Probe
		hits   map[string][]int
	}

	covered := make(map[string]snapshot, len(this.coverage))
	for file, each := range this.coverage {
		var lines LineCoverage
		for line, hit := range each.lines {
			if hit {
				lines.Covered = append(lines.Covered, line)
			} else {
				lines.Uncovered = append(lines.Uncovered, line)
			}
		}

		covered[file] = snapshot{hash: each.hash, lines: lines, probes: each.probes, hits: each.hits}
	}

	shared := make(map[string]bool, len(this.shared))
	for id, overlapped := range this.shared {
		shared[id] = overlapped
	}

	this.mutex.Unlock()
	if result.Error == "" {
		result.Error = failure
	}

	probes, hits := map[string][]rules.Probe{}, map[string]map[string][]int{}
	for file, each := range covered {
		if this.hash(file) != each.hash {
			continue
		}

		slices.Sort(each.lines.Covered)
		slices.Sort(each.lines.Uncovered)
		result.Coverage[file] = each.lines
		if len(each.probes) == 0 {
			continue
		}

		probes[file] = each.probes
		for test, indexes := range each.hits {
			if hits[test] == nil {
				hits[test] = map[string][]int{}
			}

			hits[test][file] = indexes
		}
	}

	if len(probes) > 0 && g != nil {
		modules, contracts := rules.Exercise(g, testUses(cases), probes, hits, shared)
		result.Measures = &Measures{Modules: modules, Contracts: contracts}
	}

	return result
}

// Code gathers the code some tests ran, with what their latest runs did
// and did not reach, from the files whose probes still match their text.
//
// Parameters:
//   - g: the graph of the working tree.
//   - ids: the tests to show.
//
// Returns:
//   - result: the code, by file.
func (this *Tests) Code(g *graph.Graph, ids []string) (result rules.CodeUnderTest) {
	cases, _ := this.discover(g)
	this.mutex.Lock()
	probes, hits, hashes := map[string][]rules.Probe{}, map[string]map[string][]int{}, map[string]string{}
	for file, each := range this.coverage {
		if len(each.probes) == 0 {
			continue
		}

		probes[file], hashes[file] = each.probes, each.hash
		for test, indexes := range each.hits {
			if hits[test] == nil {
				hits[test] = map[string][]int{}
			}

			hits[test][file] = indexes
		}
	}

	this.mutex.Unlock()
	for file, hash := range hashes {
		if this.hash(file) != hash {
			delete(probes, file)
			for _, files := range hits {
				delete(files, file)
			}
		}
	}

	return rules.UnderTest(g, testUses(cases), ids, probes, hits)
}

// Run starts running tests in the background and returns at once. Outcomes
// arrive in later reports.
//
// Notes:
//   - When the ids name every test of a module, the module runs whole, so
//     tests the suite could not find run too.
//
// Parameters:
//   - g: the graph of the working tree.
//   - ids: the tests to run; a subtest is named by its parent's id and its
//     own name, joined by "/", as earlier outcomes report it.
//
// Returns:
//   - err: tests are running already, or an id names no test.
//
// Errors:
//   - ErrTestsRunning: an earlier run has not finished.
//   - ErrUnknownTest: an id names no test that was found or has run.
//   - ErrNoTests: no suite runs the language of a requested test.
func (this *Tests) Run(g *graph.Graph, ids []string) error {
	cases, _ := this.discover(g)
	this.mutex.Lock()
	defer this.mutex.Unlock()
	if this.running {
		return ErrTestsRunning
	}

	requests, err := this.plan(cases, ids)
	if err != nil {
		return err
	}

	byLanguage := map[string][]TestRequest{}
	var languages []string
	for _, request := range requests {
		language, _, _ := strings.Cut(request.Module, ":")
		if this.suites[language] == nil {
			return fmt.Errorf("%w: %s", ErrNoTests, language)
		}

		if byLanguage[language] == nil {
			languages = append(languages, language)
		}

		byLanguage[language] = append(byLanguage[language], request)
	}

	for _, request := range requests {
		this.queueLocked(cases, request)
	}

	ctx, cancel := context.WithCancel(context.Background())
	this.running, this.cancel, this.runError = true, cancel, ""
	this.touchLocked()
	go func() {
		defer cancel()
		var failures []string
		for _, language := range languages {
			if err := this.runSuite(ctx, this.suites[language], byLanguage[language]); err != nil {
				failures = append(failures, err.Error())
			}
		}

		this.mutex.Lock()
		defer this.mutex.Unlock()
		this.running, this.cancel = false, nil
		this.runError = strings.Join(failures, "; ")
		this.touchLocked()
	}()

	return nil
}

// Stop cancels the run in progress, if there is one.
func (this *Tests) Stop() {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	if this.cancel != nil {
		this.cancel()
	}
}

// Version increases whenever outcomes or coverage change.
//
// Returns:
//   - result: the version.
func (this *Tests) Version() int {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	return this.version
}

// discover finds the tests of a graph's tree, once per graph.
func (this *Tests) discover(g *graph.Graph) (cases []TestCase, failure string) {
	this.discovering.Lock()
	defer this.discovering.Unlock()
	if g == nil || g == this.discovered {
		return this.cases, this.failure
	}

	var found []TestCase
	var failures []string
	for _, suite := range this.suites {
		tests, err := suite.Discover(this.root, g)
		if err != nil {
			failures = append(failures, err.Error())
		}

		found = append(found, tests...)
	}

	for index := range found {
		found[index].ID = found[index].Module + "#" + found[index].Name
	}

	slices.SortFunc(found, func(a, b TestCase) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(failures)
	this.discovered, this.cases, this.failure = g, found, strings.Join(failures, "; ")
	return this.cases, this.failure
}

// hash identifies a file's current text, or is empty when it cannot be read.
func (this *Tests) hash(file string) string {
	data, err := this.sources.Source(this.root, file)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// merge adds a run's coverage to what earlier runs covered of the same text,
// and replaces what they covered of text that has since changed. A test's
// hits in a file replace its earlier hits there, since it ran again.
func (this *Tests) merge(coverage RunCoverage) {
	hashes := make(map[string]string, len(coverage.Lines))
	for file := range coverage.Lines {
		hashes[file] = this.hash(file)
	}

	for file := range coverage.Probes {
		if _, ok := hashes[file]; !ok {
			hashes[file] = this.hash(file)
		}
	}

	this.mutex.Lock()
	defer this.mutex.Unlock()
	for id, overlapped := range coverage.Shared {
		this.shared[id] = overlapped
	}

	for file, probes := range coverage.Probes {
		each, ok := this.coverage[file]
		if !ok || each.hash != hashes[file] || len(each.probes) != len(probes) {
			each = coveredFile{hash: hashes[file], lines: map[int]bool{}}
		}

		each.probes = probes
		if each.hits == nil {
			each.hits = map[string][]int{}
		}

		for test, files := range coverage.Hits {
			if indexes, ok := files[file]; ok {
				each.hits[test] = indexes
			}
		}

		this.coverage[file] = each
	}

	for file, lines := range coverage.Lines {
		each, ok := this.coverage[file]
		if !ok || each.hash != hashes[file] {
			each = coveredFile{hash: hashes[file], lines: map[int]bool{}}
		}

		for _, line := range lines.Uncovered {
			if _, seen := each.lines[line]; !seen {
				each.lines[line] = false
			}
		}

		for _, line := range lines.Covered {
			each.lines[line] = true
		}

		this.coverage[file] = each
	}

	this.touchLocked()
}

// plan turns test ids into one request per module, in id order. A module
// whose every test is asked for runs whole.
func (this *Tests) plan(cases []TestCase, ids []string) ([]TestRequest, error) {
	known := map[string]bool{}
	perModule := map[string]int{}
	for _, each := range cases {
		known[each.ID] = true
		perModule[each.Module]++
	}

	names := map[string][]string{}
	var order []string
	for _, id := range ids {
		module, name, ok := strings.Cut(id, "#")
		top, _, _ := strings.Cut(name, "/")
		if !ok || name == "" || (!known[module+"#"+top] && this.results[id].Module == "") {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTest, id)
		}

		if names[module] == nil {
			order = append(order, module)
		}

		if !slices.Contains(names[module], name) {
			names[module] = append(names[module], name)
		}
	}

	slices.Sort(order)
	var requests []TestRequest
	for _, module := range order {
		request := TestRequest{Module: module, Names: names[module]}
		whole := 0
		for _, name := range request.Names {
			if !strings.Contains(name, "/") && known[module+"#"+name] {
				whole++
			}
		}

		if whole == perModule[module] && whole == len(request.Names) {
			request.Names = nil
		}

		requests = append(requests, request)
	}

	return requests, nil
}

// queueLocked marks the tests a request runs as queued and forgets the
// subtests they reported before, which the run reports again.
func (this *Tests) queueLocked(cases []TestCase, request TestRequest) {
	names := request.Names
	if len(names) == 0 {
		for _, each := range cases {
			if each.Module == request.Module {
				names = append(names, each.Name)
			}
		}
	}

	for _, name := range names {
		id := request.Module + "#" + name
		for other := range this.results {
			if strings.HasPrefix(other, id+"/") {
				delete(this.results, other)
			}
		}

		this.results[id] = TestOutcome{Module: request.Module, Name: name, Status: TestQueued}
	}
}

// runSuite runs one suite's requests and settles every test it was asked
// for: a module that failed as a whole fails the tests that never ran, and
// a test the run never mentioned is forgotten, as if it had not run.
func (this *Tests) runSuite(ctx context.Context, suite TestSuite, requests []TestRequest) error {
	asked := map[string]bool{}
	for _, request := range requests {
		asked[request.Module] = true
	}

	modules := map[string]TestOutcome{}
	report := func(outcome TestOutcome) {
		this.mutex.Lock()
		defer this.mutex.Unlock()
		if outcome.Name == "" {
			modules[outcome.Module] = outcome
			return
		}

		this.results[outcome.Module+"#"+outcome.Name] = outcome
		this.touchLocked()
	}

	coverage, err := suite.Run(ctx, this.root, requests, report)
	this.merge(coverage)
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for id, outcome := range this.results {
		if !asked[outcome.Module] || (outcome.Status != TestQueued && outcome.Status != TestRunning) {
			continue
		}

		module, ok := modules[outcome.Module]
		switch {
		case ctx.Err() != nil:
			delete(this.results, id)
		case ok && module.Status == TestFailed:
			this.results[id] = TestOutcome{Module: outcome.Module, Name: outcome.Name, Status: TestFailed, Output: module.Output}
		case outcome.Status == TestRunning:
			outcome.Status = TestFailed
			this.results[id] = outcome
		default:
			delete(this.results, id)
		}
	}

	this.touchLocked()
	if ctx.Err() != nil {
		return nil
	}

	return err
}

// touchLocked records a change and schedules telling the listener.
func (this *Tests) touchLocked() {
	this.version++
	if this.pending || this.changed == nil {
		return
	}

	this.pending = true
	time.AfterFunc(notifyEvery, func() {
		this.mutex.Lock()
		this.pending = false
		changed := this.changed
		this.mutex.Unlock()
		changed()
	})
}

// testUses gives the tests' ids, kinds and targets to the rules.
func testUses(cases []TestCase) (result []rules.TestUse) {
	for _, each := range cases {
		id := each.ID
		if id == "" {
			id = each.Module + "#" + each.Name
		}

		result = append(result, rules.TestUse{ID: id, Kind: each.Kind, Targets: each.Targets})
	}

	return result
}
