// Package web serves the live maps of every open project to browsers over
// HTTP on the loopback interface, pushing each rebuild with server-sent
// events, and carries each agent session's MCP stream from its shim.
package web

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

const (
	// Protocol is the version of the attach stream between a shim and the
	// server; a shim and a server that disagree refuse each other.
	Protocol = 1

	// Upgrade names the attach stream in the HTTP upgrade.
	Upgrade = "treaty-mcp"

	heartbeat = 20 * time.Second
	maxBody   = 64 * 1024

	// maxTestsBody admits a request to run every test of a large repository.
	maxTestsBody = 4 * 1024 * 1024

	joinedHeader   = "X-Treaty-Joined"
	mapHeader      = "X-Treaty-Map"
	protocolHeader = "X-Treaty-Protocol"
)

// shell is the page at the server's root: a tab per open project, each
// showing that project's live map in a frame.
//
//go:embed shell.html
var shell string

// Sessions serves one agent session's MCP stream over its attach stream.
type Sessions interface {
	// Serve answers the session's requests until in closes.
	//
	// Parameters:
	//   - project: the project the session works in.
	//   - mapURL: where the person sees the project's live map.
	//   - joined: the project was already open for another session.
	//   - in: the session's messages.
	//   - out: where replies go.
	//
	// Returns:
	//   - err: reading or writing failed.
	Serve(project *app.Project, mapURL string, joined bool, in io.Reader, out io.Writer) error
}

// Server is the one treaty server: the live map of every open project and
// the MCP stream of every agent session.
type Server struct {
	projects *app.Projects
	sessions Sessions
	version  string
	listener net.Listener
	server   *http.Server
	url      string

	stop     chan struct{}
	stopOnce sync.Once

	mutex sync.Mutex
	conns map[net.Conn]bool
}

// hello is how a treaty server identifies itself.
type hello struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// projectInfo is an open project, with where its map is.
type projectInfo struct {
	app.ProjectInfo
	URL string `json:"url"`
}

// Listen starts serving on 127.0.0.1.
//
// Notes:
//   - The port is never swapped for another, so every session and browser
//     finds the server at the same address.
//
// Parameters:
//   - projects: the open projects.
//   - sessions: serves each agent session's MCP stream.
//   - version: the treaty version, as hello reports it.
//   - port: the port to listen on; 0 picks a free one, for tests.
//
// Returns:
//   - result: the running server.
//   - err: the port could not be opened, such as when it is taken.
func Listen(projects *app.Projects, sessions Sessions, version string, port int) (result *Server, err error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}

	result = &Server{
		projects: projects,
		sessions: sessions,
		version:  version,
		listener: listener,
		url:      "http://" + listener.Addr().String(),
		stop:     make(chan struct{}),
		conns:    map[net.Conn]bool{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", result.home)
	mux.HandleFunc("GET /api/hello", result.hello)
	mux.HandleFunc("GET /api/projects", result.list)
	mux.HandleFunc("POST /api/attach", result.attach)
	mux.HandleFunc("POST /api/shutdown", result.shutdown)
	mux.HandleFunc("GET /p/{project}", result.slash)
	mux.HandleFunc("GET /p/{project}/{$}", result.page)
	routes := map[string]func(*app.Live, http.ResponseWriter, *http.Request){
		"GET /api/view":          result.view,
		"GET /api/state":         result.state,
		"GET /api/events":        result.events,
		"POST /api/baseline":     result.baseline,
		"POST /api/selection":    result.selection,
		"POST /api/show":         result.show,
		"GET /api/map-settings":  result.mapSettings,
		"POST /api/map-settings": result.saveMapSettings,
		"GET /api/positions":     result.positions,
		"POST /api/positions":    result.setPosition,
		"GET /api/preferences":   result.preferences,
		"POST /api/reclassify":   result.reclassify,
		"POST /api/preferences":  result.savePreferences,
		"POST /api/view":         result.setView,
		"POST /api/view/adopt":   result.adopt,
		"GET /api/tests":         result.tests,
		"POST /api/tests/run":    result.runTests,
		"POST /api/tests/code":   result.testCode,
		"POST /api/tests/stop":   result.stopTests,
	}

	for route, handler := range routes {
		method, path, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /p/{project}"+path, result.project(handler))
	}

	result.server = &http.Server{Handler: result.guard(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = result.server.Serve(listener) }()
	return result, nil
}

// Close stops serving and ends every session's stream.
//
// Returns:
//   - err: the listener could not be closed.
func (this *Server) Close() error {
	err := this.server.Close()
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for conn := range this.conns {
		_ = conn.Close()
	}

	return err
}

// MapURL is where the person sees a project's live map: the server's one
// page, with that project's tab shown.
//
// Parameters:
//   - slug: the project's URL segment.
//
// Returns:
//   - result: the map's URL.
func (this *Server) MapURL(slug string) (result string) {
	return this.url + "/?p=" + url.QueryEscape(slug)
}

// PageURL is where a project's own map page is served, which the server's
// page shows in that project's tab and whose API requests are relative to
// it.
//
// Parameters:
//   - slug: the project's URL segment.
//
// Returns:
//   - result: the page's URL.
func (this *Server) PageURL(slug string) (result string) {
	return this.url + "/p/" + url.PathEscape(slug) + "/"
}

// Stopped closes when someone asks the server to stop, as treaty restart
// does.
//
// Returns:
//   - result: the channel.
func (this *Server) Stopped() <-chan struct{} {
	return this.stop
}

// URL is where the server listens.
//
// Returns:
//   - result: the base URL.
func (this *Server) URL() string {
	return this.url
}

// adopt makes the previewed architecture treaty.yaml's.
func (this *Server) adopt(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var ignored struct{}
	if !decode(writer, request, &ignored) {
		return
	}

	if _, err := live.AdoptView(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(live, writer, request)
}

// attach joins an agent session to its directory's project and carries its
// MCP stream over the upgraded connection until either side closes it. The
// session leaves the project when the stream ends, however it ends.
func (this *Server) attach(writer http.ResponseWriter, request *http.Request) {
	if !strings.EqualFold(request.Header.Get("Upgrade"), Upgrade) {
		http.Error(writer, "expected an upgrade to "+Upgrade, http.StatusUpgradeRequired)
		return
	}

	if got := request.Header.Get(protocolHeader); got != strconv.Itoa(Protocol) {
		http.Error(writer, fmt.Sprintf("this server speaks protocol %d, the shim %q", Protocol, got), http.StatusConflict)
		return
	}

	root := request.URL.Query().Get("root")
	if !filepath.IsAbs(root) {
		http.Error(writer, "root must be an absolute directory", http.StatusBadRequest)
		return
	}

	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "upgrades unsupported", http.StatusInternalServerError)
		return
	}

	project, joined, detach, err := this.projects.Attach(root)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	defer detach()
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}

	this.mutex.Lock()
	this.conns[conn] = true
	this.mutex.Unlock()
	defer func() {
		this.mutex.Lock()
		delete(this.conns, conn)
		this.mutex.Unlock()
		_ = conn.Close()
	}()

	mapURL := this.MapURL(project.Slug)
	_, _ = fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n%s: %s\r\n%s: %t\r\n\r\n", Upgrade, mapHeader, mapURL, joinedHeader, joined)
	if err := buffered.Flush(); err != nil {
		return
	}

	_ = this.sessions.Serve(project, mapURL, joined, buffered.Reader, conn)
}

func (this *Server) baseline(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var baseline app.Baseline
	if !decode(writer, request, &baseline) {
		return
	}

	if err := live.SetBaseline(baseline); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(live, writer, request)
}

// events streams the state after every rebuild as server-sent events. The
// browser fetches the view when the version changes.
func (this *Server) events(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	updates, cancel := live.Subscribe()
	defer cancel()
	send := func(state app.LiveState) bool {
		data, _ := json.Marshal(state)
		if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
			return false
		}

		flusher.Flush()
		return true
	}

	if !send(live.State()) {
		return
	}

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case state := <-updates:
			if !send(state) {
				return
			}
		case <-ticker.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}

// guard refuses requests addressed to another host name, which is how DNS
// rebinding reaches a loopback server.
func (this *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(this.listener.Addr().String())
	allowed := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !allowed[request.Host] {
			http.Error(writer, "forbidden host", http.StatusForbidden)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

// hello tells a shim that a treaty server answers here, and which.
func (this *Server) hello(writer http.ResponseWriter, _ *http.Request) {
	respond(writer, hello{App: "treaty", Version: this.version, Protocol: Protocol})
}

// home serves the page with a tab per open project, showing the one a
// session joined most recently unless the address names another.
func (this *Server) home(writer http.ResponseWriter, _ *http.Request) {
	latest, _ := json.Marshal(this.projects.Latest())
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(writer, strings.Replace(shell, `/*LATEST*/""`, string(latest), 1))
}

// list serves the open projects and where their maps are; with a root
// query, only the project of that directory.
func (this *Server) list(writer http.ResponseWriter, request *http.Request) {
	result := []projectInfo{}
	root := request.URL.Query().Get("root")
	if root != "" {
		if project, err := this.projects.FindRoot(root); err == nil {
			root = project.Root
		}
	}

	for _, info := range this.projects.List() {
		if root == "" || info.Root == root {
			result = append(result, projectInfo{ProjectInfo: info, URL: this.MapURL(info.Slug)})
		}
	}

	respond(writer, result)
}

// mapSettings serves the person's choices on this repository's map.
func (this *Server) mapSettings(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	settings, err := live.MapSettings()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, settings)
}

// page serves the live page of one project, titled with its name.
func (this *Server) page(writer http.ResponseWriter, request *http.Request) {
	project, err := this.projects.Find(request.PathValue("project"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}

	title := "<title>" + html.EscapeString(project.Name) + " · Treaty</title>"
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(writer, strings.Replace(string(project.Live.Page()), "<title>Treaty</title>", title, 1))
}

// positions serves where the person put modules on the map.
func (this *Server) positions(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	positions, err := live.Positions()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, positions)
}

// preferences serves the person's saved choices on the map.
func (this *Server) preferences(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	preferences, err := live.Preferences()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, preferences)
}

// project routes a request to the live graph of the project in its path.
func (this *Server) project(handler func(*app.Live, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		project, err := this.projects.Find(request.PathValue("project"))
		if err != nil {
			http.Error(writer, err.Error(), http.StatusNotFound)
			return
		}

		handler(project.Live, writer, request)
	}
}

// runTests starts running the tests named, answering with the report that
// shows them queued.
func (this *Server) runTests(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}

	if !decodeUpTo(writer, request, &body, maxTestsBody) {
		return
	}

	switch err := live.RunTests(body.IDs); {
	case errors.Is(err, app.ErrTestsRunning):
		http.Error(writer, err.Error(), http.StatusConflict)
	case errors.Is(err, app.ErrUnknownTest):
		http.Error(writer, err.Error(), http.StatusBadRequest)
	case errors.Is(err, app.ErrNoTests):
		http.Error(writer, err.Error(), http.StatusNotFound)
	case errors.Is(err, app.ErrNotReady):
		http.Error(writer, err.Error(), http.StatusServiceUnavailable)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	default:
		this.tests(live, writer, request)
	}
}

// testCode serves the code the tests named ran, with what their latest runs
// did and did not reach.
func (this *Server) testCode(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}

	if !decodeUpTo(writer, request, &body, maxTestsBody) {
		return
	}

	code, err := live.TestCode(body.IDs)
	switch {
	case errors.Is(err, app.ErrNoTests):
		http.Error(writer, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusServiceUnavailable)
	default:
		respond(writer, code)
	}
}

// saveMapSettings merges the fields and region names sent into the saved
// choices on this repository's map.
func (this *Server) saveMapSettings(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var update app.MapSettings
	if !decode(writer, request, &update) {
		return
	}

	settings, err := live.SaveMapSettings(update)
	if errors.Is(err, app.ErrMapSettings) {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, settings)
}

// savePreferences merges the fields sent into the saved choices.
func (this *Server) savePreferences(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var update app.Preferences
	if !decode(writer, request, &update) {
		return
	}

	preferences, err := live.SavePreferences(update)
	if errors.Is(err, app.ErrPreferences) {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, preferences)
}

// reclassify moves a module to another layer in treaty.yaml.
func (this *Server) reclassify(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Module string `json:"module"`
		Layer  string `json:"layer"`
		Side   string `json:"side"`
	}

	if !decode(writer, request, &body) {
		return
	}

	switch _, err := live.Reclassify(body.Module, rules.Placement{Layer: body.Layer, Side: body.Side}); {
	case errors.Is(err, app.ErrPreviewing), errors.Is(err, app.ErrPlacement), errors.Is(err, rules.ErrArchitecture):
		http.Error(writer, err.Error(), http.StatusBadRequest)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	default:
		this.state(live, writer, request)
	}
}

func (this *Server) selection(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var selection app.Selection
	if !decode(writer, request, &selection) {
		return
	}

	live.SetSelection(selection)
	writer.WriteHeader(http.StatusNoContent)
}

// setPosition saves where the person put a module, or forgets it when the
// body has no position.
func (this *Server) setPosition(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Architecture string        `json:"architecture"`
		Module       string        `json:"module"`
		Position     *app.Position `json:"position"`
	}

	if !decode(writer, request, &body) {
		return
	}

	positions, err := live.SetPosition(body.Architecture, body.Module, body.Position)
	if errors.Is(err, rules.ErrArchitecture) {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, positions)
}

// setView draws another architecture, or treaty.yaml's again.
func (this *Server) setView(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var view struct {
		Architecture string `json:"architecture"`
	}

	if !decode(writer, request, &view) {
		return
	}

	if err := live.SetView(view.Architecture); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(live, writer, request)
}

func (this *Server) show(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Target string `json:"target"`
		Reason string `json:"reason"`
	}

	if !decode(writer, request, &body) {
		return
	}

	switch err := live.Show(body.Target, body.Reason); {
	case errors.Is(err, app.ErrTooSoon):
		http.Error(writer, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, app.ErrUnknownTarget):
		http.Error(writer, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusBadRequest)
	default:
		this.state(live, writer, request)
	}
}

// shutdown stops the server; running shims start a new one and rejoin.
func (this *Server) shutdown(writer http.ResponseWriter, request *http.Request) {
	var ignored struct{}
	if !decode(writer, request, &ignored) {
		return
	}

	this.stopOnce.Do(func() { close(this.stop) })
	writer.WriteHeader(http.StatusNoContent)
}

// slash sends a project's address without its trailing slash to the page,
// whose requests are relative to it.
func (this *Server) slash(writer http.ResponseWriter, request *http.Request) {
	http.Redirect(writer, request, this.PageURL(request.PathValue("project")), http.StatusMovedPermanently)
}

func (this *Server) state(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(live.State())
}

// stopTests cancels the test run in progress.
func (this *Server) stopTests(live *app.Live, writer http.ResponseWriter, request *http.Request) {
	var ignored struct{}
	if !decode(writer, request, &ignored) {
		return
	}

	live.StopTests()
	writer.WriteHeader(http.StatusNoContent)
}

// tests serves the tests, their latest outcomes and their coverage.
func (this *Server) tests(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	report, err := live.Tests()
	switch {
	case errors.Is(err, app.ErrNoTests):
		http.Error(writer, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusServiceUnavailable)
	default:
		respond(writer, report)
	}
}

func (this *Server) view(live *app.Live, writer http.ResponseWriter, _ *http.Request) {
	data, version := live.Payload()
	if data == nil {
		http.Error(writer, "the map is still being built", http.StatusServiceUnavailable)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Treaty-Version", fmt.Sprint(version))
	_, _ = writer.Write(data)
}

// decode reads a JSON body. Requiring the JSON content type means another
// web page cannot post here without a CORS preflight, which is never
// granted.
func decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	return decodeUpTo(writer, request, target, maxBody)
}

// decodeUpTo reads a JSON body of at most limit bytes, as decode does.
func decodeUpTo(writer http.ResponseWriter, request *http.Request, target any, limit int64) bool {
	if media, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type")); media != "application/json" {
		http.Error(writer, "expected application/json", http.StatusUnsupportedMediaType)
		return false
	}

	if err := json.NewDecoder(io.LimitReader(request.Body, limit)).Decode(target); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return false
	}

	return true
}

// respond writes a value as JSON that no cache keeps.
func respond(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(value)
}
