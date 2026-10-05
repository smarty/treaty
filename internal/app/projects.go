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

var ErrUnknownProject = errors.New("no such project is open")

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
// while at least one session works in it.
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
}

// ProjectInfo describes an open project.
type ProjectInfo struct {
	Name     string `json:"name"`
	Root     string `json:"root"`
	Slug     string `json:"slug"`
	Sessions int    `json:"sessions"`
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

	project.sessions++
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
	var once sync.Once
	detach = func() { once.Do(func() { this.detach(project) }) }
	if project.err != nil {
		detach()
		return nil, false, nil, project.err
	}

	return project, joined, detach, nil
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
		result = append(result, ProjectInfo{Name: project.Name, Root: project.Root, Slug: project.Slug, Sessions: project.sessions})
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
// was the last.
func (this *Projects) detach(project *Project) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	project.sessions--
	if project.sessions > 0 {
		return
	}

	if project.err != nil {
		this.removeLocked(project)
		return
	}

	project.expire = time.AfterFunc(this.grace, func() {
		this.mutex.Lock()
		defer this.mutex.Unlock()
		if project.sessions == 0 && this.byRoot[project.Root] == project {
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
