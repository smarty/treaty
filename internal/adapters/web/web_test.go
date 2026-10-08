package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/adapters/gitvcs"
	"github.com/smarty/treaty/internal/adapters/htmlmap"
	"github.com/smarty/treaty/internal/adapters/language/golang"
	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

func TestLiveServer(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	write(t, root, "treaty.yaml", "layers:\n  domain: [\"core/**\"]\n  adapter:\n    driven: [\"store/**\"]\n")
	write(t, root, "core/order.go", "package core\n\n// Order is a purchase.\ntype Order struct{ ID string }\n")
	write(t, root, "store/store.go", "package store\n\nimport \"example.com/shop/core\"\n\nfunc Save(order core.Order) error { return nil }\n")
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "first")

	workspace := filesystem.NewWorkspace(root)
	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), workspace, htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	live := app.NewLive(service, filesystem.NewWatcher(root))
	stop := make(chan struct{})
	defer close(stop)
	live.Start(stop)
	_, base := listen(t, root, live)
	var state app.LiveState
	if err := json.Unmarshal(get(t, base+"/api/state", http.StatusOK), &state); err != nil || state.Baseline.Mode != app.BaselineHead || state.Baseline.Commit == "" || state.Error != "" {
		t.Fatalf("state: %+v %v", state, err)
	}

	response := get(t, base+"/api/view", http.StatusOK)
	var view struct {
		Modules []app.MapModule `json:"modules"`
	}

	if err := json.Unmarshal(response, &view); err != nil || len(view.Modules) != 2 {
		t.Fatalf("view: %s %v", response, err)
	}

	if page := get(t, base+"/", http.StatusOK); !bytes.Contains(page, []byte("/*DATA*/null")) {
		t.Fatal("the live page must load its data from the server")
	}

	if page := get(t, base+"/", http.StatusOK); !bytes.Contains(page, []byte("<title>"+filepath.Base(root)+" · Treaty</title>")) || !bytes.Contains(page, []byte(`rel="icon"`)) {
		t.Fatal("the live page must be titled with its project and carry the icon")
	}

	// Editing a file rebuilds the map, pushes the new version to browsers,
	// and the change shows against HEAD.
	stream, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stream.Body.Close() }()
	events := bufio.NewScanner(stream.Body)
	next := func() app.LiveState {
		for events.Scan() {
			if data, ok := strings.CutPrefix(events.Text(), "data: "); ok {
				var pushed app.LiveState
				_ = json.Unmarshal([]byte(data), &pushed)
				return pushed
			}
		}

		t.Fatal("the event stream ended")
		return app.LiveState{}
	}

	if first := next(); first.Version != state.Version {
		t.Fatalf("first event: %+v", first)
	}

	write(t, root, "core/order.go", "package core\n\n// Order is a purchase.\ntype Order struct{ ID string }\n\n// Total sums an order.\nfunc Total(order Order) int { return 0 }\n")
	if pushed := next(); pushed.Version <= state.Version {
		t.Fatalf("pushed: %+v", pushed)
	}

	changes, err := live.Changes()
	if err != nil || !strings.Contains(changes, "added") || !strings.Contains(changes, "go:core:Total") {
		t.Fatalf("changes: %s %v", changes, err)
	}

	// A new import that points outward shows as a new violation.
	write(t, root, "core/leak.go", "package core\n\nimport \"example.com/shop/store\"\n\nvar saver = store.Save\n")
	waitFor(t, func() bool { text, _ := live.Changes(); return strings.Contains(text, "new layer violations") })

	post(t, base+"/api/baseline", "application/json", `{"mode":"ref","ref":"-x"}`, http.StatusBadRequest)
	post(t, base+"/api/baseline", "application/x-www-form-urlencoded", `mode=head`, http.StatusUnsupportedMediaType)
	post(t, base+"/api/baseline", "application/json", `{"mode":"ref","ref":"HEAD"}`, http.StatusOK)
	if next := live.State(); next.Baseline.Mode != app.BaselineRef || next.Baseline.Ref != "HEAD" {
		t.Fatalf("baseline: %+v", next.Baseline)
	}

	post(t, base+"/api/selection", "application/json", `{"type":"module","id":"go:core"}`, http.StatusNoContent)
	selection, slice, err := live.Selected()
	if err != nil || selection.ID != "go:core" || slice == nil {
		t.Fatalf("selection: %+v %v %v", selection, slice, err)
	}

	request, _ := http.NewRequest(http.MethodGet, base+"/api/state", nil)
	request.Host = "attacker.example"
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("another host name must be refused: %v %v", response, err)
	}

	// A plan is saved as a design and overlaid on the map, unbuilt.
	report, err := live.Plan("pricing", "go:core/pricing.go\n  func Price(order Order) int\n")
	if err != nil || len(report.Items) != 1 || report.Items[0].Kind != app.ItemMissing {
		t.Fatalf("plan: %+v %v", report, err)
	}

	if _, err := os.Stat(filepath.Join(root, ".treaty", "designs", "pricing.pen")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(get(t, base+"/api/view", http.StatusOK), []byte(`"id":"go:core:Price"`)) {
		t.Fatal("the plan must be overlaid on the map")
	}

	// Show offers a symbol, even a planned one, and refuses a second request
	// soon after.
	if err := live.Show("go:nowhere:X", "no such thing"); !errors.Is(err, app.ErrUnknownTarget) {
		t.Fatalf("unknown target: %v", err)
	}

	if err := live.Show("go:core:Price", "this is where pricing goes"); err != nil {
		t.Fatal(err)
	}

	if pointer := live.State().Pointer; pointer == nil || pointer.Sequence != 1 || pointer.Target != "go:core:Price" {
		t.Fatalf("pointer: %+v", pointer)
	}

	if err := live.Show("go:core:Order", "too soon"); !errors.Is(err, app.ErrTooSoon) {
		t.Fatalf("second show: %v", err)
	}
}

func get(t *testing.T, url string, status int) []byte {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = response.Body.Close() }()
	var body bytes.Buffer
	_, _ = body.ReadFrom(response.Body)
	if response.StatusCode != status {
		t.Fatalf("GET %s: %d %s", url, response.StatusCode, body.String())
	}

	return body.Bytes()
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
}

// listen serves one project's live graph, answering at the base URL of its
// map.
func listen(t *testing.T, root string, live *app.Live) (server *Server, base string) {
	t.Helper()
	projects := app.NewProjects(func(string) (*app.Live, func(), error) { return live, func() {}, nil }, time.Minute)
	server, err := Listen(projects, nil, "test", 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = server.Close() })
	project, _, detach, err := projects.Attach(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(detach)
	return server, strings.TrimSuffix(server.PageURL(project.Slug), "/")
}

func post(t *testing.T, url, contentType, body string, status int) {
	t.Helper()
	response, err := http.Post(url, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("POST %s %s: %d, want %d", url, body, response.StatusCode, status)
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if done() {
			return
		}
	}

	t.Fatal("timed out")
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchitectureView(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	write(t, root, "treaty.yaml", "layers:\n  domain: [\"core/**\"]\n  adapter:\n    driven: [\"store/**\"]\n")
	write(t, root, "core/order.go", "package core\n\ntype Order struct{ ID string }\n")
	write(t, root, "store/store.go", "package store\n\nimport \"example.com/shop/core\"\n\nfunc Save(order core.Order) error { return nil }\n")
	git(t, root, "init", "-q")

	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	live := app.NewLive(service, filesystem.NewWatcher(root))
	stop := make(chan struct{})
	defer close(stop)
	live.Start(stop)
	_, base := listen(t, root, live)
	architecture := func() string {
		var view struct {
			Architecture string `json:"architecture"`
		}

		_ = json.Unmarshal(get(t, base+"/api/view", http.StatusOK), &view)
		return view.Architecture
	}

	// Previewing redraws the map, while checks and agents keep following
	// treaty.yaml.
	post(t, base+"/api/view", "application/json", `{"architecture":"onion"}`, http.StatusBadRequest)
	post(t, base+"/api/view", "application/json", `{"architecture":"layered"}`, http.StatusOK)
	state := live.State()
	if state.View.Architecture != "layered" || state.View.Configured != "hexagonal" || state.View.AdoptAt.IsZero() {
		t.Fatalf("view: %+v", state.View)
	}

	if got := architecture(); got != "layered" {
		t.Fatalf("the map should draw layered, not %q", got)
	}

	if overview, _ := live.Overview(); !strings.Contains(overview, "adapters may not use each other") {
		t.Fatalf("agents must keep following treaty.yaml:\n%s", overview)
	}

	// Choosing treaty.yaml's architecture again ends the preview.
	post(t, base+"/api/view", "application/json", `{"architecture":"hexagonal"}`, http.StatusOK)
	if view := live.State().View; view.Architecture != "hexagonal" || !view.AdoptAt.IsZero() {
		t.Fatalf("view: %+v", view)
	}

	// Adopting replaces treaty.yaml.
	post(t, base+"/api/view/adopt", "application/json", `{}`, http.StatusBadRequest)
	post(t, base+"/api/view", "application/json", `{"architecture":"clean"}`, http.StatusOK)
	post(t, base+"/api/view/adopt", "application/json", `{}`, http.StatusOK)
	if data, _ := os.ReadFile(filepath.Join(root, "treaty.yaml")); !strings.Contains(string(data), "architecture: clean") {
		t.Fatalf("treaty.yaml:\n%s", data)
	}

	if view := live.State().View; view.Configured != "clean" || !view.AdoptAt.IsZero() {
		t.Fatalf("view: %+v", view)
	}

	// A preview left alone becomes treaty.yaml.
	live.SetAdoptAfter(200 * time.Millisecond)
	post(t, base+"/api/view", "application/json", `{"architecture":"layered"}`, http.StatusOK)
	waitFor(t, func() bool { return live.State().View.Configured == "layered" })
	if data, _ := os.ReadFile(filepath.Join(root, "treaty.yaml")); !strings.Contains(string(data), "architecture: layered") {
		t.Fatalf("treaty.yaml:\n%s", data)
	}

	if got := architecture(); got != "layered" {
		t.Fatalf("the map should draw layered, not %q", got)
	}
}

func TestPreferences(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	write(t, root, "core/order.go", "package core\n\ntype Order struct{ ID string }\n")
	settings := filepath.Join(t.TempDir(), "settings.json")
	start := func() (string, func()) {
		service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(settings))
		live := app.NewLive(service, filesystem.NewWatcher(root))
		stop := make(chan struct{})
		live.Start(stop)
		server, base := listen(t, root, live)
		return base, func() { _ = server.Close(); close(stop) }
	}

	base, stop := start()
	if got := string(get(t, base+"/api/preferences", http.StatusOK)); strings.TrimSpace(got) != "{}" {
		t.Fatalf("nothing saved yet: %s", got)
	}

	// Each save merges into what is kept.
	post(t, base+"/api/preferences", "application/json", `{"theme":"vampire","layout":{"center":{"tabs":["map"]}}}`, http.StatusOK)
	post(t, base+"/api/preferences", "application/json", `{"follow":true}`, http.StatusOK)
	post(t, base+"/api/preferences", "application/json", `{"legend":false}`, http.StatusOK)
	post(t, base+"/api/preferences", "application/json", `{"layout":[1,2]}`, http.StatusBadRequest)
	post(t, base+"/api/preferences", "application/x-www-form-urlencoded", `theme=x`, http.StatusUnsupportedMediaType)
	stop()

	// Another server, as on another port or in another repository, sees them.
	base, stop = start()
	defer stop()
	var saved app.Preferences
	if err := json.Unmarshal(get(t, base+"/api/preferences", http.StatusOK), &saved); err != nil {
		t.Fatal(err)
	}

	if saved.Theme != "vampire" || saved.Follow == nil || !*saved.Follow || saved.Legend == nil || *saved.Legend || !strings.Contains(string(saved.Layout), `"map"`) {
		t.Fatalf("saved: %+v %s", saved, saved.Layout)
	}
}

func TestMapSettings(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	write(t, root, "core/order.go", "package core\n\ntype Order struct{ ID string }\n")
	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(filepath.Join(t.TempDir(), "settings.json")))
	live := app.NewLive(service, filesystem.NewWatcher(root))
	stop := make(chan struct{})
	defer close(stop)
	live.Start(stop)
	_, base := listen(t, root, live)

	if got := string(get(t, base+"/api/map-settings", http.StatusOK)); strings.TrimSpace(got) != "{}" {
		t.Fatalf("nothing saved yet: %s", got)
	}

	// Each save merges into what is kept, and an empty name forgets one.
	post(t, base+"/api/map-settings", "application/json", `{"follow":true}`, http.StatusOK)
	post(t, base+"/api/map-settings", "application/json", `{"internals":true,"names":{"slices":{"orders":"Ordering","billing":"Money"}}}`, http.StatusOK)
	post(t, base+"/api/map-settings", "application/json", `{"names":{"slices":{"billing":""}}}`, http.StatusOK)
	post(t, base+"/api/map-settings", "application/json", `{"names":{"nowhere":{"a":"b"}}}`, http.StatusBadRequest)
	post(t, base+"/api/map-settings", "application/json", `{"names":{"slices":{"a":"line\nbreak"}}}`, http.StatusBadRequest)

	var saved app.MapSettings
	if err := json.Unmarshal(get(t, base+"/api/map-settings", http.StatusOK), &saved); err != nil {
		t.Fatal(err)
	}

	if saved.Follow == nil || !*saved.Follow || saved.Internals == nil || !*saved.Internals || len(saved.Names) != 1 || len(saved.Names["slices"]) != 1 || saved.Names["slices"]["orders"] != "Ordering" {
		t.Fatalf("saved: %+v", saved)
	}

	// They belong to the repository, kept in its workspace.
	if _, err := os.Stat(filepath.Join(root, ".treaty", "map.json")); err != nil {
		t.Fatal(err)
	}

	// The project's preferences, kept for every repository, are apart.
	if got := string(get(t, base+"/api/preferences", http.StatusOK)); strings.TrimSpace(got) != "{}" {
		t.Fatalf("the map's own settings are not preferences: %s", got)
	}
}

func TestMoveModules(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	write(t, root, "treaty.yaml", "# Our rings.\narchitecture: hexagonal\nlayers:\n  domain: [\"core/**\"]\n  adapter:\n    driven: [\"store/**\"]\n")
	write(t, root, "core/order.go", "package core\n\ntype Order struct{ ID string }\n")
	write(t, root, "store/store.go", "package store\n\nimport \"example.com/shop/core\"\n\nfunc Save(order core.Order) error { return nil }\n")
	write(t, root, "web/web.go", "package web\n\nimport \"example.com/shop/core\"\n\nvar Home core.Order\n")
	git(t, root, "init", "-q")

	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	live := app.NewLive(service, filesystem.NewWatcher(root))
	stop := make(chan struct{})
	defer close(stop)
	live.Start(stop)
	_, base := listen(t, root, live)
	layer := func(id string) string {
		var view struct {
			Modules []app.MapModule `json:"modules"`
		}

		_ = json.Unmarshal(get(t, base+"/api/view", http.StatusOK), &view)
		for _, module := range view.Modules {
			if module.ID == id {
				return module.Layer + "/" + module.Side
			}
		}

		return ""
	}

	// Dropping an unclassified module on a ring declares it there.
	if got := layer("go:web"); got != "unclassified/" {
		t.Fatalf("web starts unclassified, not %s", got)
	}

	post(t, base+"/api/reclassify", "application/json", `{"module":"go:web","layer":"adapter","side":"driving"}`, http.StatusOK)
	if got := layer("go:web"); got != "adapter/driving" {
		t.Fatalf("web should be a driving adapter, not %s", got)
	}

	if data, _ := os.ReadFile(filepath.Join(root, "treaty.yaml")); !strings.Contains(string(data), "# Our rings.") || !strings.Contains(string(data), `driving: ["web"]`) {
		t.Fatalf("treaty.yaml:\n%s", data)
	}

	post(t, base+"/api/reclassify", "application/json", `{"module":"go:web","layer":"adapter"}`, http.StatusBadRequest)
	post(t, base+"/api/reclassify", "application/json", `{"module":"go:web","layer":"presentation"}`, http.StatusBadRequest)
	post(t, base+"/api/reclassify", "application/json", `{"module":"go:nowhere","layer":"domain"}`, http.StatusBadRequest)

	// While another architecture is previewed, layers stay as they are.
	post(t, base+"/api/view", "application/json", `{"architecture":"layered"}`, http.StatusOK)
	post(t, base+"/api/reclassify", "application/json", `{"module":"go:web","layer":"domain"}`, http.StatusBadRequest)
	post(t, base+"/api/view", "application/json", `{"architecture":"hexagonal"}`, http.StatusOK)

	// Positions are kept per architecture, and forgotten on request.
	post(t, base+"/api/positions", "application/json", `{"architecture":"hexagonal","module":"go:web","position":{"x":-120.5,"y":40}}`, http.StatusOK)
	post(t, base+"/api/positions", "application/json", `{"architecture":"layered","module":"go:core","position":{"x":10,"y":20}}`, http.StatusOK)
	post(t, base+"/api/positions", "application/json", `{"architecture":"onion","module":"go:core","position":{"x":10,"y":20}}`, http.StatusBadRequest)
	var positions app.Positions
	_ = json.Unmarshal(get(t, base+"/api/positions", http.StatusOK), &positions)
	if positions["hexagonal"]["go:web"] != (app.Position{X: -120.5, Y: 40}) || positions["layered"]["go:core"] != (app.Position{X: 10, Y: 20}) {
		t.Fatalf("positions: %+v", positions)
	}

	post(t, base+"/api/positions", "application/json", `{"architecture":"layered","module":"go:core"}`, http.StatusOK)
	positions = nil
	_ = json.Unmarshal(get(t, base+"/api/positions", http.StatusOK), &positions)
	if _, ok := positions["layered"]; ok || len(positions["hexagonal"]) != 1 {
		t.Fatalf("forgetting a position: %+v", positions)
	}
}

func TestRunTests(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/calc\n\ngo 1.22\n")
	write(t, root, "treaty.yaml", "architecture: none\n")
	write(t, root, "calc/calc.go", "package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n")
	write(t, root, "calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n")
	extractor := golang.NewExtractor()
	service := app.NewService(root, filesystem.NewConfig(root), extractor, []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	live := app.NewLive(service, filesystem.NewWatcher(root))
	live.UseTests(app.NewTests(root, extractor, []app.TestSuite{golang.NewTestSuite()}))
	if err := live.Refresh(); err != nil {
		t.Fatal(err)
	}

	_, base := listen(t, root, live)
	report := func(data []byte) (result app.TestReport) {
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("report: %s %v", data, err)
		}

		return result
	}

	if found := report(get(t, base+"/api/tests", http.StatusOK)); len(found.Tests) != 1 || found.Tests[0].ID != "go:calc#TestAdd" {
		t.Fatalf("tests: %+v", found)
	}

	post(t, base+"/api/tests/run", "application/json", `{"ids":["go:calc#TestMissing"]}`, http.StatusBadRequest)
	post(t, base+"/api/tests/run", "application/json", `{"ids":["go:calc#TestAdd"]}`, http.StatusOK)

	deadline := time.Now().Add(60 * time.Second)
	for {
		done := report(get(t, base+"/api/tests", http.StatusOK))
		if !done.Running {
			if done.Results["go:calc#TestAdd"].Status != app.TestPassed || len(done.Coverage["calc/calc.go"].Covered) == 0 || done.Error != "" {
				t.Fatalf("done: %+v", done)
			}

			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the run did not finish")
		}

		time.Sleep(50 * time.Millisecond)
	}

	response, err := http.Post(base+"/api/tests/code", "application/json", strings.NewReader(`{"ids":["go:calc#TestAdd"]}`))
	if err != nil {
		t.Fatal(err)
	}

	var code rules.CodeUnderTest
	err = json.NewDecoder(response.Body).Decode(&code)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(code.Ran) != 1 || len(code.Files) != 1 || code.Files[0].File != "calc/calc.go" || code.Files[0].Reached != code.Files[0].Probes || code.Files[0].Probes == 0 {
		t.Fatalf("the code TestAdd ran: %d %+v %v", response.StatusCode, code, err)
	}

	post(t, base+"/api/tests/stop", "application/json", `{}`, http.StatusNoContent)
	if state := live.State(); state.Tests == 0 {
		t.Fatalf("the state counts test changes: %+v", state)
	}
}
