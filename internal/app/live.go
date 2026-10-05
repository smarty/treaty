package app

import (
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
	// AdoptAfter is how long the map may preview another architecture
	// before treaty.yaml switches to it.
	AdoptAfter = 5 * time.Minute

	BaselineHead        = "head"
	BaselinePullRequest = "pr"
	BaselineRef         = "ref"

	ShowEvery = 15 * time.Second

	baselineEvery = 4
	cachedBases   = 4

	// The tree is checked for changes twice a second, unless it is large:
	// LargeRepository files or more, or a check that takes slowWalk or
	// longer. Then it is checked every five seconds.
	LargeRepository = 20000
	pollFast        = 500 * time.Millisecond
	pollSlow        = 5 * time.Second
	slowWalk        = 250 * time.Millisecond
)

var (
	ErrBaseline  = errors.New("unknown baseline mode")
	ErrNoPreview = errors.New("the map is showing the architecture in treaty.yaml")
	ErrNotReady  = errors.New("the live graph has not been built")
	ErrTooSoon   = errors.New("not shown: the person was shown something moments ago")
)

// Baseline is what the live map compares the working tree with.
type Baseline struct {
	// Mode is head, pr or ref.
	Mode string `json:"mode"`

	// Ref is the revision compared with in ref mode.
	Ref string `json:"ref,omitempty"`

	// Target is the branch a pull request merges into, in pr mode. Empty
	// means the repository's default branch.
	Target string `json:"target,omitempty"`

	// Commit is the resolved commit, empty when there is nothing to compare
	// with, such as outside a git repository.
	Commit string `json:"commit,omitempty"`

	// Label describes the baseline for people.
	Label string `json:"label"`
}

// Selection is what the person has selected on the live map. Type is
// symbol, module, file, group or edge; a file's ID is its module's id and
// its path, joined by "|".
type Selection struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// LiveState is the live map's state, as the browser sees it.
type LiveState struct {
	Version   int       `json:"version"`
	Baseline  Baseline  `json:"baseline"`
	View      View      `json:"view"`
	Selection Selection `json:"selection"`
	Pointer   *Pointer  `json:"pointer,omitempty"`
	Error     string    `json:"error,omitempty"`

	// Tests increases whenever test outcomes or coverage change, so the
	// browser knows to fetch them again.
	Tests int `json:"tests,omitempty"`
}

// View is the architecture the live map draws. Another architecture than
// the one in treaty.yaml is a preview: the map shows the code fitted to it,
// as treaty init would propose, while checks and agents keep following
// treaty.yaml. A preview becomes treaty.yaml when the person adopts it, or
// after AdoptAfter.
type View struct {
	// Architecture is the architecture drawn.
	Architecture string `json:"architecture"`

	// Configured is the architecture in treaty.yaml.
	Configured string `json:"configured"`

	// AdoptAt is when treaty.yaml switches to the previewed architecture;
	// zero when nothing is previewed.
	AdoptAt time.Time `json:"adopt_at,omitzero"`
}

// Pointer is something the agent asked the person to look at. The map
// offers it; it moves the view only when the person accepts or has chosen
// to follow the agent.
type Pointer struct {
	// Sequence increases with every pointer, so the page shows each once.
	Sequence int `json:"sequence"`

	// Target is a symbol or module id.
	Target string `json:"target"`

	// Reason says why, in the agent's words.
	Reason string `json:"reason"`
}

// Live keeps the graph of the working tree in memory, rebuilds it whenever a
// file changes, and serves it to the browser and to coding agents.
type Live struct {
	service *Service
	watcher Watcher
	tests   *Tests

	building sync.Mutex
	saving   sync.Mutex

	mutex       sync.Mutex
	baseline    Baseline
	selection   Selection
	analysis    *analysis
	payload     []byte
	version     int
	failure     string
	fingerprint string
	bases       map[string]baseRevision
	baseOrder   []string
	subscribers map[chan LiveState]bool
	pointer     *Pointer
	shownAt     time.Time
	showEvery   time.Duration
	configured  string
	preview     string
	adoptAt     time.Time
	adoptAfter  time.Duration
	positions   sync.Mutex
}

// NewLive creates the live graph for a repository. Nothing is built until
// Start or Refresh.
//
// Parameters:
//   - service: the use cases and adapters.
//   - watcher: notices changes to the working tree.
//
// Returns:
//   - result: the live graph.
func NewLive(service *Service, watcher Watcher) *Live {
	return &Live{
		service:     service,
		watcher:     watcher,
		baseline:    Baseline{Mode: BaselineHead},
		bases:       map[string]baseRevision{},
		subscribers: map[chan LiveState]bool{},
		showEvery:   ShowEvery,
		adoptAfter:  AdoptAfter,
	}
}

// AdoptView replaces treaty.yaml with the previewed architecture, as
// treaty init would propose it, and rebuilds.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: nothing is previewed, or treaty.yaml could not be written.
//
// Errors:
//   - ErrNoPreview: the map already shows treaty.yaml's architecture.
func (this *Live) AdoptView() (path string, err error) {
	this.mutex.Lock()
	style := this.preview
	this.mutex.Unlock()
	if style == "" {
		return "", ErrNoPreview
	}

	if path, err = this.service.adopt(style); err != nil {
		return "", err
	}

	this.mutex.Lock()
	if this.preview == style {
		this.preview, this.adoptAt = "", time.Time{}
	}

	this.mutex.Unlock()
	return path, this.Refresh()
}

// Page returns the live page, which loads its data from the server.
//
// Returns:
//   - result: the HTML page.
func (this *Live) Page() []byte {
	return this.service.renderer.Page()
}

// Payload returns the latest map data.
//
// Returns:
//   - data: the encoded view and layout, or nil before the first build.
//   - version: increases with every rebuild.
func (this *Live) Payload() (data []byte, version int) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	return this.payload, this.version
}

// Refresh rebuilds the graph, the analysis and the map data now, and tells
// every subscriber. A failure keeps the last good build and is reported in
// the state.
//
// Returns:
//   - err: the build failed.
func (this *Live) Refresh() error {
	this.building.Lock()
	defer this.building.Unlock()
	fingerprint, _, _ := this.watcher.Fingerprint()
	this.mutex.Lock()
	baseline, preview := this.baseline, this.preview
	this.mutex.Unlock()

	resolved, err := this.resolve(baseline)
	var built *analysis
	var payload []byte
	if err == nil {
		built, payload, err = this.build(resolved, preview)
	}

	this.mutex.Lock()
	this.fingerprint = fingerprint
	this.version++
	this.failure = ""
	if err != nil {
		this.failure = err.Error()
	} else {
		this.baseline, this.analysis, this.payload = resolved, built, payload
		this.configured = built.config.Architecture.Style
		if this.preview == this.configured {
			this.preview, this.adoptAt = "", time.Time{}
		}
	}

	this.notifyLocked()
	this.mutex.Unlock()
	return err
}

// RunTests starts running tests against the latest build; their outcomes
// arrive in later test reports.
//
// Parameters:
//   - ids: the tests to run, as test reports name them.
//
// Returns:
//   - err: there is no build or no test bench, tests are running, or an id
//     names no test.
//
// Errors:
//   - ErrNotReady: the live graph has not been built.
//   - ErrNoTests: no test bench is in use.
//   - ErrTestsRunning: an earlier run has not finished.
//   - ErrUnknownTest: an id names no test.
func (this *Live) RunTests(ids []string) error {
	if this.tests == nil {
		return ErrNoTests
	}

	current, err := this.current()
	if err != nil {
		return err
	}

	return this.tests.Run(current.head, ids)
}

// SetAdoptAfter changes how long the map previews another architecture
// before treaty.yaml switches to it. A preview already counting down keeps
// its time.
//
// Parameters:
//   - after: the preview's length; AdoptAfter by default.
func (this *Live) SetAdoptAfter(after time.Duration) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	this.adoptAfter = after
}

// SetBaseline changes what the working tree is compared with, and rebuilds.
//
// Parameters:
//   - baseline: the mode, and the ref or target it needs.
//
// Returns:
//   - err: the mode is unknown or the baseline cannot be resolved; the
//     previous baseline stays in place.
//
// Errors:
//   - ErrBaseline: the mode is not head, pr or ref.
func (this *Live) SetBaseline(baseline Baseline) error {
	resolved, err := this.resolve(baseline)
	if err != nil {
		return err
	}

	this.mutex.Lock()
	this.baseline = resolved
	this.mutex.Unlock()
	return this.Refresh()
}

// SetView draws another architecture, or treaty.yaml's again, and rebuilds.
// Another architecture than treaty.yaml's starts the AdoptAfter countdown;
// choosing again restarts it.
//
// Parameters:
//   - architecture: one of rules.Styles.
//
// Returns:
//   - err: the architecture is unknown, or the rebuild failed.
//
// Errors:
//   - rules.ErrArchitecture: architecture is not one of rules.Styles.
func (this *Live) SetView(architecture string) error {
	if !slices.Contains(rules.Styles, architecture) {
		return fmt.Errorf("%w: unknown architecture %q; use one of %s", rules.ErrArchitecture, architecture, strings.Join(rules.Styles, ", "))
	}

	this.mutex.Lock()
	if architecture == this.configured {
		this.preview, this.adoptAt = "", time.Time{}
	} else {
		this.preview, this.adoptAt = architecture, time.Now().Add(this.adoptAfter)
	}

	this.mutex.Unlock()
	return this.Refresh()
}

// SetSelection records what the person selected on the map.
//
// Parameters:
//   - selection: the selection, empty when nothing is selected.
func (this *Live) SetSelection(selection Selection) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	this.selection = selection
}

// Show asks the person to look at a symbol or module. The map offers it
// without moving the view, unless the person has chosen to follow the
// agent. Requests closer together than ShowEvery are refused, so the view
// never keeps shifting under the person.
//
// Parameters:
//   - target: a symbol id or module id, or a short name that matches one.
//   - reason: why the person should look, shown beside the offer.
//
// Returns:
//   - err: the target does not exist, or the last request was too recent.
//
// Errors:
//   - ErrUnknownTarget: no symbol or module matches the target.
//   - ErrAmbiguousTarget: several symbols or modules match it.
//   - ErrTooSoon: another request came less than ShowEvery ago.
func (this *Live) Show(target, reason string) error {
	current, err := this.current()
	if err != nil {
		return err
	}

	known := current.head.Symbol(target) != nil || current.head.Module(target) != nil
	if !known && !this.planned(current, target) {
		if target, err = current.resolve(target); err != nil {
			return err
		}

		if current.head.Symbol(target) == nil && current.head.Module(target) == nil {
			return fmt.Errorf("%w: %s is a file; show takes a symbol or module", ErrUnknownTarget, target)
		}
	}

	this.mutex.Lock()
	defer this.mutex.Unlock()
	if wait := this.showEvery - time.Since(this.shownAt); !this.shownAt.IsZero() && wait > 0 {
		return fmt.Errorf("%w; try again in %ds, or leave it", ErrTooSoon, int(wait.Seconds()+0.999))
	}

	sequence := 1
	if this.pointer != nil {
		sequence = this.pointer.Sequence + 1
	}

	this.pointer = &Pointer{Sequence: sequence, Target: target, Reason: reason}
	this.shownAt = time.Now()
	this.notifyLocked()
	return nil
}

// Start builds the graph and keeps it current until stop closes, polling
// the working tree for changes and the baseline for new commits.
//
// Parameters:
//   - stop: closing it ends the polling.
func (this *Live) Start(stop <-chan struct{}) {
	// Starting a server refreshes the default themes; a failure only means
	// the map uses the built-in copies.
	_ = this.service.themes.Install()
	_ = this.Refresh()
	go this.poll(stop)
}

// State returns the current baseline, selection and version.
//
// Returns:
//   - result: the state.
func (this *Live) State() LiveState {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	return this.stateLocked()
}

// StopTests cancels the test run in progress, if there is one.
func (this *Live) StopTests() {
	if this.tests != nil {
		this.tests.Stop()
	}
}

// Subscribe delivers the state after every rebuild. Only the latest state
// is kept for a slow subscriber.
//
// Returns:
//   - updates: receives each new state.
//   - cancel: stops delivery.
func (this *Live) Subscribe() (updates <-chan LiveState, cancel func()) {
	channel := make(chan LiveState, 1)
	this.mutex.Lock()
	this.subscribers[channel] = true
	this.mutex.Unlock()
	return channel, func() {
		this.mutex.Lock()
		delete(this.subscribers, channel)
		this.mutex.Unlock()
	}
}

// Tests reports the tests of the latest build, their latest outcomes and
// the coverage that still matches the files.
//
// Returns:
//   - result: the report.
//   - err: there is no build or no test bench.
//
// Errors:
//   - ErrNotReady: the live graph has not been built.
//   - ErrNoTests: no test bench is in use.
func (this *Live) Tests() (result TestReport, err error) {
	if this.tests == nil {
		return TestReport{}, ErrNoTests
	}

	current, err := this.current()
	if err != nil {
		return TestReport{}, err
	}

	return this.tests.Report(current.head), nil
}

// UseTests lets the live map find and run tests, and tells subscribers
// whenever outcomes or coverage change. Call it before Start.
//
// Parameters:
//   - tests: the test bench.
func (this *Live) UseTests(tests *Tests) {
	this.tests = tests
	tests.OnChange(func() {
		this.mutex.Lock()
		defer this.mutex.Unlock()
		this.notifyLocked()
	})
}

// baseRevision is a cached baseline: its graph and the text of its files,
// which are only read.
type baseRevision struct {
	graph   *graph.Graph
	sources map[string]string
}

func (this *Live) baseGraph(commit string) (*graph.Graph, map[string]string, error) {
	this.mutex.Lock()
	cached, ok := this.bases[commit]
	this.mutex.Unlock()
	if ok {
		return cached.graph.Clone(), cached.sources, nil
	}

	built, sources, err := this.service.graphAt(commit)
	if err != nil {
		return nil, nil, err
	}

	this.mutex.Lock()
	this.bases[commit] = baseRevision{graph: built, sources: sources}
	this.baseOrder = append(this.baseOrder, commit)
	if len(this.baseOrder) > cachedBases {
		delete(this.bases, this.baseOrder[0])
		this.baseOrder = this.baseOrder[1:]
	}

	this.mutex.Unlock()
	return built.Clone(), sources, nil
}

// build analyzes the working tree against a baseline. The analysis follows
// treaty.yaml; the map data shows the preview architecture instead, when
// there is one.
func (this *Live) build(baseline Baseline, preview string) (*analysis, []byte, error) {
	config, _, err := this.service.config.Load()
	if err != nil {
		return nil, nil, err
	}

	head, err := this.service.extractor.Extract(this.service.root)
	if err != nil {
		return nil, nil, err
	}

	var base *graph.Graph
	var baseSources map[string]string
	if baseline.Commit != "" {
		if base, baseSources, err = this.baseGraph(baseline.Commit); err != nil {
			return nil, nil, err
		}
	}

	result := this.service.analyzeGraphs(config, head, base, baseline.Label)
	result.baseSources = baseSources
	shown := result
	if preview != "" && preview != config.Architecture.Style {
		fitted := config
		fitted.Architecture, _ = propose(head, preview)
		var previewBase *graph.Graph
		if base != nil {
			previewBase = base.Clone()
		}

		shown = this.service.analyzeGraphs(fitted, head.Clone(), previewBase, baseline.Label)
		shown.baseSources = baseSources
	}

	designs, err := this.service.workspace.Designs()
	if err != nil {
		return nil, nil, err
	}

	view, err := this.service.buildView(shown, designs, true)
	if err != nil {
		return nil, nil, err
	}

	payload, err := this.service.renderer.Payload(view)
	return result, payload, err
}

// current returns the latest analysis.
func (this *Live) current() (*analysis, error) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	if this.analysis == nil {
		if this.failure != "" {
			return nil, fmt.Errorf("%w: %s", ErrNotReady, this.failure)
		}

		return nil, ErrNotReady
	}

	return this.analysis, nil
}

func (this *Live) poll(stop <-chan struct{}) {
	timer := time.NewTimer(pollFast)
	defer timer.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-stop:
			return
		case <-timer.C:
		}

		started := time.Now()
		fingerprint, files, err := this.watcher.Fingerprint()
		timer.Reset(PollEvery(files, time.Since(started)))
		this.mutex.Lock()
		changed := err == nil && fingerprint != this.fingerprint
		baseline := this.baseline
		this.mutex.Unlock()
		if !changed && tick%baselineEvery == 0 {
			if resolved, err := this.resolve(baseline); err == nil && resolved.Commit != baseline.Commit {
				changed = true
			}
		}

		this.mutex.Lock()
		due := !this.adoptAt.IsZero() && time.Now().After(this.adoptAt)
		this.mutex.Unlock()
		if due {
			if _, err := this.AdoptView(); err != nil && !errors.Is(err, ErrNoPreview) {
				this.mutex.Lock()
				this.adoptAt, this.failure = time.Time{}, "treaty.yaml was not switched: "+err.Error()
				this.notifyLocked()
				this.mutex.Unlock()
			}

			continue
		}

		if changed {
			_ = this.Refresh()
		}
	}
}

// resolve finds the commit a baseline compares with. Outside a git
// repository, or before its first commit, HEAD resolves to nothing, and the
// map shows no changes.
func (this *Live) resolve(baseline Baseline) (Baseline, error) {
	vcs := this.service.vcs
	result := Baseline{Mode: baseline.Mode}
	switch baseline.Mode {
	case BaselineHead, "":
		result.Mode = BaselineHead
		commit, err := vcs.Resolve("HEAD")
		if err != nil {
			result.Label = "nothing: HEAD has no commit"
			return result, nil
		}

		result.Commit, result.Label = commit, "HEAD "+short(commit)
	case BaselinePullRequest:
		target := baseline.Target
		if target == "" {
			branch, err := vcs.DefaultBranch()
			if err != nil {
				return Baseline{}, err
			}

			target = branch
		}

		commit, err := vcs.MergeBase("HEAD", target)
		if err != nil {
			return Baseline{}, err
		}

		result.Target, result.Commit = baseline.Target, commit
		result.Label = fmt.Sprintf("pull request into %s (merge base %s)", target, short(commit))
	case BaselineRef:
		commit, err := vcs.Resolve(baseline.Ref)
		if err != nil {
			return Baseline{}, err
		}

		result.Ref, result.Commit = baseline.Ref, commit
		result.Label = fmt.Sprintf("%s (%s)", baseline.Ref, short(commit))
	default:
		return Baseline{}, fmt.Errorf("%w: %q", ErrBaseline, baseline.Mode)
	}

	return result, nil
}

func (this *Live) notifyLocked() {
	state := this.stateLocked()
	for subscriber := range this.subscribers {
		select {
		case <-subscriber:
		default:
		}

		subscriber <- state
	}
}

// planned reports whether a target is an unbuilt item of some design, which
// the map draws even though the graph does not have it.
func (this *Live) planned(current *analysis, target string) bool {
	designs, _ := this.service.workspace.Designs()
	for _, name := range designs {
		walk, err := this.service.checkDesign(current, name)
		if err != nil {
			continue
		}

		for _, element := range walk.elements {
			if element.ID == target || element.Module == target {
				return true
			}
		}
	}

	return false
}

func (this *Live) stateLocked() LiveState {
	view := View{Architecture: this.configured, Configured: this.configured, AdoptAt: this.adoptAt}
	if this.preview != "" {
		view.Architecture = this.preview
	}

	result := LiveState{Version: this.version, Baseline: this.baseline, View: view, Selection: this.selection, Pointer: this.pointer, Error: this.failure}
	if this.tests != nil {
		result.Tests = this.tests.Version()
	}

	return result
}

// PollEvery says how long to wait between checks of the tree for changes:
// pollFast for small and medium repositories, and pollSlow for large ones,
// whose walk costs more, so watching never becomes the work.
//
// Parameters:
//   - files: how many files the last check covered.
//   - took: how long the last check took.
//
// Returns:
//   - result: the wait before the next check.
func PollEvery(files int, took time.Duration) (result time.Duration) {
	if files >= LargeRepository || took >= slowWalk {
		return pollSlow
	}

	return pollFast
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}

	return commit
}
