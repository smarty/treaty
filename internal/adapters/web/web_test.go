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
	"github.com/smarty/treaty/internal/adapters/golang"
	"github.com/smarty/treaty/internal/adapters/htmlmap"
	"github.com/smarty/treaty/internal/app"
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
	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), workspace, htmlmap.New(), filesystem.NewAgentConfig(root))
	live := app.NewLive(service, filesystem.NewWatcher(root), nil)
	stop := make(chan struct{})
	defer close(stop)
	live.Start(stop)
	server, err := Listen(live, 0)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()
	peer := NewPeer(server.URL())
	state, err := peer.State()
	if err != nil || state.Baseline.Mode != app.BaselineHead || state.Baseline.Commit == "" || state.Error != "" {
		t.Fatalf("state: %+v %v", state, err)
	}

	response := get(t, server.URL()+"/api/view", http.StatusOK)
	var view struct {
		Modules []app.MapModule `json:"modules"`
	}

	if err := json.Unmarshal(response, &view); err != nil || len(view.Modules) != 2 {
		t.Fatalf("view: %s %v", response, err)
	}

	if page := get(t, server.URL()+"/", http.StatusOK); !bytes.Contains(page, []byte("/*DATA*/null")) {
		t.Fatal("the live page must load its data from the server")
	}

	// Editing a file rebuilds the map, pushes the new version to browsers,
	// and the change shows against HEAD.
	stream, err := http.Get(server.URL() + "/api/events")
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

	post(t, server.URL()+"/api/baseline", "application/json", `{"mode":"ref","ref":"-x"}`, http.StatusBadRequest)
	post(t, server.URL()+"/api/baseline", "application/x-www-form-urlencoded", `mode=head`, http.StatusUnsupportedMediaType)
	post(t, server.URL()+"/api/baseline", "application/json", `{"mode":"ref","ref":"HEAD"}`, http.StatusOK)
	if next, _ := peer.State(); next.Baseline.Mode != app.BaselineRef || next.Baseline.Ref != "HEAD" {
		t.Fatalf("baseline: %+v", next.Baseline)
	}

	post(t, server.URL()+"/api/selection", "application/json", `{"type":"module","id":"go:core"}`, http.StatusNoContent)
	selection, slice, err := live.Selected()
	if err != nil || selection.ID != "go:core" || slice == nil {
		t.Fatalf("selection: %+v %v %v", selection, slice, err)
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL()+"/api/state", nil)
	request.Host = "attacker.example"
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("another host name must be refused: %v %v", response, err)
	}

	// A plan is saved as a design and overlaid on the map, unbuilt.
	report, err := live.Plan("pricing", "go:core/pricing.go\n  func Price(order Order) int\n")
	if err != nil || len(report.Items) != 1 || report.Items[0].Kind != app.ItemMissing {
		t.Fatalf("plan: %+v %v", report, err)
	}

	if _, err := os.Stat(filepath.Join(root, ".treaty", "designs", "pricing.cml")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(get(t, server.URL()+"/api/view", http.StatusOK), []byte(`"id":"go:core:Price"`)) {
		t.Fatal("the plan must be overlaid on the map")
	}

	// A second session follows the first server's baseline and selection.
	follower := app.NewLive(service, filesystem.NewWatcher(root), peer)
	follower.Start(stop)
	if followed, _, _ := follower.Selected(); followed.ID != "go:core" || follower.State().Baseline.Mode != app.BaselineRef {
		t.Fatalf("follower: %+v %+v", followed, follower.State().Baseline)
	}

	// Show offers a symbol, even a planned one, and refuses a second request
	// soon after, including one forwarded by a following session.
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

	if err := follower.Show("go:core:Order", "too soon, from another session"); !errors.Is(err, app.ErrTooSoon) {
		t.Fatalf("forwarded show: %v", err)
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
