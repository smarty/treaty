package app

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/smarty/treaty/internal/graph"
)

const (
	BaselineHead        = "head"
	BaselinePullRequest = "pr"
	BaselineRef         = "ref"

	ShowEvery = 15 * time.Second

	baselineEvery = 4
	cachedBases   = 4
	pollInterval  = 500 * time.Millisecond
)

var (
	ErrBaseline = errors.New("unknown baseline mode")
	ErrNotReady = errors.New("the live graph has not been built")
	ErrTooSoon  = errors.New("not shown: the person was shown something moments ago")
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

// Selection is what the person has selected on the live map.
type Selection struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// LiveState is the live map's state, as the browser and a peer see it.
type LiveState struct {
	Version   int       `json:"version"`
	Baseline  Baseline  `json:"baseline"`
	Selection Selection `json:"selection"`
	Pointer   *Pointer  `json:"pointer,omitempty"`
	Error     string    `json:"error,omitempty"`
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
	peer    Peer

	building sync.Mutex

	mutex       sync.Mutex
	baseline    Baseline
	selection   Selection
	analysis    *analysis
	payload     []byte
	version     int
	failure     string
	fingerprint string
	bases       map[string]*graph.Graph
	baseOrder   []string
	subscribers map[chan LiveState]bool
	pointer     *Pointer
	shownAt     time.Time
	showEvery   time.Duration
}

// NewLive creates the live graph for a repository. Nothing is built until
// Start or Refresh.
//
// Parameters:
//   - service: the use cases and adapters.
//   - watcher: notices changes to the working tree.
//   - peer: another server already serving this repository's map, whose
//     baseline and selection this one follows; nil when there is none.
//
// Returns:
//   - result: the live graph.
func NewLive(service *Service, watcher Watcher, peer Peer) *Live {
	return &Live{
		service:     service,
		watcher:     watcher,
		peer:        peer,
		baseline:    Baseline{Mode: BaselineHead},
		bases:       map[string]*graph.Graph{},
		subscribers: map[chan LiveState]bool{},
		showEvery:   ShowEvery,
	}
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
	fingerprint, _ := this.watcher.Fingerprint()
	this.mutex.Lock()
	baseline := this.baseline
	this.mutex.Unlock()

	resolved, err := this.resolve(baseline)
	var built *analysis
	var payload []byte
	if err == nil {
		built, payload, err = this.build(resolved)
	}

	this.mutex.Lock()
	this.fingerprint = fingerprint
	this.version++
	this.failure = ""
	if err != nil {
		this.failure = err.Error()
	} else {
		this.baseline, this.analysis, this.payload = resolved, built, payload
	}

	this.notifyLocked()
	this.mutex.Unlock()
	return err
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
//   - target: a symbol id or module id.
//   - reason: why the person should look, shown beside the offer.
//
// Returns:
//   - err: the target does not exist, or the last request was too recent.
//
// Errors:
//   - ErrUnknownTarget: no symbol or module has that id.
//   - ErrTooSoon: another request came less than ShowEvery ago.
func (this *Live) Show(target, reason string) error {
	if this.peer != nil {
		return this.peer.Show(target, reason)
	}

	current, err := this.current()
	if err != nil {
		return err
	}

	if current.head.Symbol(target) == nil && current.head.Module(target) == nil && !this.planned(current, target) {
		return fmt.Errorf("%w: %s", ErrUnknownTarget, target)
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
	this.sync()
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

func (this *Live) baseGraph(commit string) (*graph.Graph, error) {
	this.mutex.Lock()
	cached := this.bases[commit]
	this.mutex.Unlock()
	if cached != nil {
		return cached.Clone(), nil
	}

	built, err := this.service.graphAt(commit)
	if err != nil {
		return nil, err
	}

	this.mutex.Lock()
	this.bases[commit] = built
	this.baseOrder = append(this.baseOrder, commit)
	if len(this.baseOrder) > cachedBases {
		delete(this.bases, this.baseOrder[0])
		this.baseOrder = this.baseOrder[1:]
	}

	this.mutex.Unlock()
	return built.Clone(), nil
}

func (this *Live) build(baseline Baseline) (*analysis, []byte, error) {
	config, _, err := this.service.config.Load()
	if err != nil {
		return nil, nil, err
	}

	head, err := this.service.extractor.Extract(this.service.root)
	if err != nil {
		return nil, nil, err
	}

	var base *graph.Graph
	if baseline.Commit != "" {
		if base, err = this.baseGraph(baseline.Commit); err != nil {
			return nil, nil, err
		}
	}

	result := this.service.analyzeGraphs(config, head, base, baseline.Label)
	designs, err := this.service.workspace.Designs()
	if err != nil {
		return nil, nil, err
	}

	view, err := this.service.buildView(result, designs, true)
	if err != nil {
		return nil, nil, err
	}

	payload, err := this.service.renderer.Payload(view)
	return result, payload, err
}

// current returns the latest analysis, after following the peer.
func (this *Live) current() (*analysis, error) {
	this.sync()
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
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		fingerprint, err := this.watcher.Fingerprint()
		this.mutex.Lock()
		changed := err == nil && fingerprint != this.fingerprint
		baseline := this.baseline
		this.mutex.Unlock()
		if !changed && tick%baselineEvery == 0 {
			this.sync()
			if resolved, err := this.resolve(baseline); err == nil && resolved.Commit != baseline.Commit {
				changed = true
			}
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
	return LiveState{Version: this.version, Baseline: this.baseline, Selection: this.selection, Pointer: this.pointer, Error: this.failure}
}

// sync follows the peer's baseline and selection, when there is a peer.
func (this *Live) sync() {
	if this.peer == nil {
		return
	}

	state, err := this.peer.State()
	if err != nil {
		return
	}

	this.mutex.Lock()
	this.selection = state.Selection
	same := state.Baseline.Mode == this.baseline.Mode && state.Baseline.Ref == this.baseline.Ref && state.Baseline.Target == this.baseline.Target
	this.mutex.Unlock()
	if !same {
		_ = this.SetBaseline(state.Baseline)
	}
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}

	return commit
}
