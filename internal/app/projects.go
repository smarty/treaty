package app

import (
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// ProjectGrace is how long a project stays open after its last session
// leaves, and how long the server stays up with no project open, so a
// session that reconnects or restarts keeps its tab.
const ProjectGrace = 30 * time.Second

var (
	ErrUnknownProject = errors.New("no such project is open")
	ErrProjectInUse   = errors.New("an agent session is working in the project")
)

// unsafeSlug matches what a project's URL segment leaves out.
var unsafeSlug = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Opener builds and starts the live graph of one repository.
//
// Parameters:
//   - root: the repository's directory.
//
// Returns:
//   - live: the started live graph.
//   - close: stops it.
//   - err: the live graph could not be built.
type Opener func(root string) (live *Live, close func(), err error)

// Project is one repository the shared server keeps a live graph of, open
// while at least one agent session works in it or while the person keeps
// it open, from treaty open until they close its tab.
type Project struct {
	// Live is the repository's live graph.
	Live *Live

	// Name is what the person sees: the repository's folder, with its parent
	// when another open project has a folder of the same name.
	Name string

	// Root is the repository's directory.
	Root string

	// Slug names the project in the map's URLs.
	Slug string

	close    func()
	err      error
	expire   *time.Timer
	ready    chan struct{}
	sessions int
	pinned   bool
}

// ProjectInfo describes an open project: Sessions counts the agent
// sessions working in it, and Pinned says the person opened it, with treaty
// open, so it stays open until they close it.
type ProjectInfo struct {
	Name     string `json:"name"`
	Root     string `json:"root"`
	Slug     string `json:"slug"`
	Sessions int    `json:"sessions"`
	Pinned   bool   `json:"pinned"`
}

// Projects is every repository the shared server keeps open, one per
// directory however many sessions work there. A project closes ProjectGrace
// after its last session leaves.
type Projects struct {
	open  Opener
	grace time.Duration

	mutex     sync.Mutex
	byRoot    map[string]*Project
	order     []string
	latest    string
	idle      chan struct{}
	idleTimer *time.Timer
	idled     bool
}

// NewProjects creates an empty registry. Its Idle channel closes when it has
// had no project open for grace, starting now.
//
// Parameters:
//   - open: builds a repository's live graph.
//   - grace: how long a project outlives its last session, and the registry
//     its last project.
//
// Returns:
//   - result: the registry.
func NewProjects(open Opener, grace time.Duration) *Projects {
	result := &Projects{open: open, grace: grace, byRoot: map[string]*Project{}, idle: make(chan struct{})}
	result.mutex.Lock()
	result.armIdleLocked()
	result.mutex.Unlock()
	return result
}

// Attach joins a session to the project of a directory, opening it when no
// session works there yet.
//
// Notes:
//   - The directory is resolved through symbolic links, so two sessions in
//     one repository share its project however they reached it.
//   - Opening builds the graph, so the first session waits for it.
//
// Parameters:
//   - root: the session's directory.
//
// Returns:
//   - project: the project.
//   - joined: the project was already open for another session.
//   - detach: leaves the project; call it once, when the session ends.
//   - err: the directory could not be resolved or its graph built.
func (this *Projects) Attach(root string) (project *Project, joined bool, detach func(), err error) {
	project, joined, err = this.hold(root, func(project *Project) { project.sessions++ })
	if project == nil {
		return nil, false, nil, err
	}

	var once sync.Once
	detach = func() { once.Do(func() { this.detach(project) }) }
	if err != nil {
		detach()
		return nil, false, nil, err
	}

	return project, joined, detach, nil
}

// Open keeps the project of a directory open for the person, without an
// agent session, until Close: what treaty open does.
//
// Parameters:
//   - root: the repository's directory.
//
// Returns:
//   - project: the project.
//   - opened: the person had not kept it open already.
//   - err: the directory could not be resolved or its graph built.
func (this *Projects) Open(root string) (project *Project, opened bool, err error) {
	project, _, err = this.hold(root, func(project *Project) { opened = !project.pinned; project.pinned = true })
	if err != nil {
		if project != nil {
			this.mutex.Lock()
			project.pinned = false
			this.releaseLocked(project)
			this.mutex.Unlock()
		}

		return nil, false, err
	}

	return project, opened, nil
}

// Close closes a project the person kept open, once no agent session works
// in it. Closing the last project lets the server stop after its grace.
//
// Parameters:
//   - slug: the project's URL segment.
//
// Returns:
//   - err: no such project is open, or an agent works in it.
//
// Errors:
//   - ErrUnknownProject: no open project has that slug.
//   - ErrProjectInUse: an agent session works in it; it closes when the
//     session ends.
func (this *Projects) Close(slug string) error {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for _, project := range this.byRoot {
		if project.Slug != slug {
			continue
		}

		if project.sessions > 0 {
			return ErrProjectInUse
		}

		project.pinned = false
		if project.expire != nil {
			project.expire.Stop()
			project.expire = nil
		}

		this.removeLocked(project)
		return nil
	}

	return ErrUnknownProject
}

// hold opens the project of a directory when it is not open yet, applies
// claim to it, and waits for its graph.
//
// Returns:
//   - project: the project, nil when root could not be used.
//   - joined: the project was already open.
//   - err: its graph could not be built.
func (this *Projects) hold(root string, claim func(*Project)) (project *Project, joined bool, err error) {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	root = filepath.Clean(root)
	this.mutex.Lock()
	project, joined = this.byRoot[root]
	if !joined {
		project = &Project{Root: root, ready: make(chan struct{})}
		project.Name, project.Slug = this.namesLocked(root)
		this.byRoot[root] = project
		this.order = append(this.order, root)
	}

	claim(project)
	if project.expire != nil {
		project.expire.Stop()
		project.expire = nil
	}

	if this.idleTimer != nil {
		this.idleTimer.Stop()
		this.idleTimer = nil
	}

	this.latest = root
	this.mutex.Unlock()

	if !joined {
		live, closer, err := this.open(root)
		this.mutex.Lock()
		project.Live, project.close, project.err = live, closer, err
		this.mutex.Unlock()
		close(project.ready)
	}

	<-project.ready
	return project, joined, project.err
}

// Find looks up an open project by its slug.
//
// Parameters:
//   - slug: the project's URL segment.
//
// Returns:
//   - result: the project.
//   - err: no open project has that slug.
//
// Errors:
//   - ErrUnknownProject: no open project has that slug.
func (this *Projects) Find(slug string) (result *Project, err error) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for _, project := range this.byRoot {
		if project.Slug == slug && project.Live != nil && project.err == nil {
			return project, nil
		}
	}

	return nil, ErrUnknownProject
}

// FindRoot looks up the open project of a directory.
//
// Parameters:
//   - root: the directory.
//
// Returns:
//   - result: the project.
//   - err: no project is open there.
//
// Errors:
//   - ErrUnknownProject: no project is open there.
func (this *Projects) FindRoot(root string) (result *Project, err error) {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	this.mutex.Lock()
	defer this.mutex.Unlock()
	if project, ok := this.byRoot[filepath.Clean(root)]; ok && project.Live != nil && project.err == nil {
		return project, nil
	}

	return nil, ErrUnknownProject
}

// Idle closes when the registry has had no project open for its grace.
//
// Returns:
//   - result: the channel.
func (this *Projects) Idle() <-chan struct{} {
	return this.idle
}

// Latest is the project a session joined most recently, which the map shows
// first.
//
// Returns:
//   - result: its slug, empty when nothing is open.
func (this *Projects) Latest() (result string) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	if project, ok := this.byRoot[this.latest]; ok {
		return project.Slug
	}

	if len(this.order) > 0 {
		return this.byRoot[this.order[len(this.order)-1]].Slug
	}

	return ""
}

// List describes the open projects in the order they opened.
//
// Returns:
//   - result: the projects.
func (this *Projects) List() (result []ProjectInfo) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	result = []ProjectInfo{}
	for _, root := range this.order {
		project := this.byRoot[root]
		result = append(result, ProjectInfo{Name: project.Name, Root: project.Root, Slug: project.Slug, Sessions: project.sessions, Pinned: project.pinned})
	}

	return result
}

// armIdleLocked starts the countdown to Idle when nothing is open.
func (this *Projects) armIdleLocked() {
	if len(this.byRoot) > 0 || this.idled || this.idleTimer != nil {
		return
	}

	this.idleTimer = time.AfterFunc(this.grace, func() {
		this.mutex.Lock()
		defer this.mutex.Unlock()
		if len(this.byRoot) == 0 && !this.idled {
			this.idled = true
			close(this.idle)
		}
	})
}

// detach ends one session, and closes the project after the grace when it
// was the last and the person does not keep the project open.
func (this *Projects) detach(project *Project) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	project.sessions--
	this.releaseLocked(project)
}

// releaseLocked closes a project nothing holds any more: at once when it
// failed to build, else after the grace.
func (this *Projects) releaseLocked(project *Project) {
	if project.sessions > 0 || project.pinned || this.byRoot[project.Root] != project {
		return
	}

	if project.err != nil {
		this.removeLocked(project)
		return
	}

	project.expire = time.AfterFunc(this.grace, func() {
		this.mutex.Lock()
		defer this.mutex.Unlock()
		if project.sessions == 0 && !project.pinned && this.byRoot[project.Root] == project {
			this.removeLocked(project)
		}
	})
}

// namesLocked chooses a new project's name and slug, apart from every open
// project's.
func (this *Projects) namesLocked(root string) (name, slug string) {
	base := filepath.Base(root)
	name = base
	taken := map[string]bool{}
	for _, project := range this.byRoot {
		taken[project.Slug] = true
		if filepath.Base(project.Root) == base {
			name = filepath.Join(filepath.Base(filepath.Dir(root)), base)
		}
	}

	stem := unsafeSlug.ReplaceAllString(base, "-")
	if stem == "" || stem == "." || stem == ".." {
		stem = "project"
	}

	slug = stem
	for n := 2; taken[slug]; n++ {
		slug = stem + "-" + strconv.Itoa(n)
	}

	return name, slug
}

// removeLocked closes a project and forgets it.
func (this *Projects) removeLocked(project *Project) {
	delete(this.byRoot, project.Root)
	for i, root := range this.order {
		if root == project.Root {
			this.order = append(this.order[:i], this.order[i+1:]...)
			break
		}
	}

	if project.close != nil {
		go project.close()
	}

	this.armIdleLocked()
}
