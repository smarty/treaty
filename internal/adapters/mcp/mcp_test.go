package mcp

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/adapters/gitvcs"
	"github.com/smarty/treaty/internal/adapters/golang"
	"github.com/smarty/treaty/internal/adapters/htmlmap"
	"github.com/smarty/treaty/internal/app"
)

func TestSliceMatchesTheCommand(t *testing.T) {
	root := fixture(t)
	server, service := serve(t, root)
	want, err := service.Slice("go:injection:Container.Resolve")
	if err != nil {
		t.Fatal(err)
	}

	data, _ := json.MarshalIndent(want, "", "  ")
	if got := callText(t, server, "slice", map[string]string{"target": "go:injection:Container.Resolve", "format": "json"}); !strings.HasPrefix(got, string(data)) {
		t.Fatalf("the MCP slice must be treaty slice's JSON:\ngot:\n%s\nwant:\n%s", got, data)
	}

	if got := callText(t, server, "slice", map[string]string{"target": "Container.Resolve"}); !strings.HasPrefix(got, want.Text()) {
		t.Fatalf("the MCP slice text must be treaty slice's text:\ngot:\n%s\nwant:\n%s", got, want.Text())
	}
}

func TestViolationWarnings(t *testing.T) {
	root := fixture(t)
	server, _ := serve(t, root)

	// The fixture's one violation is news on the first call, then told.
	if got := callText(t, server, "find", map[string]string{"query": "Order"}); !strings.Contains(got, "1 layer violation(s) you have not been told about") || !strings.Contains(got, "go:internal/graph → go:adapters/reflectx") {
		t.Fatalf("the first call must warn about the existing violation:\n%s", got)
	}

	if got := callText(t, server, "find", map[string]string{"query": "Order"}); strings.Contains(got, "layer violation") {
		t.Fatalf("a violation is told once:\n%s", got)
	}

	// An edit that adds a violation surfaces on the next call, whatever it is.
	write(t, root, "internal/sorting/clock.go", "package sorting\n\nimport \"github.com/smarty/injection/adapters/memory\"\n\nvar store memory.Store\n")
	if err := server.live.Refresh(); err != nil {
		t.Fatal(err)
	}

	got := callText(t, server, "source", map[string]string{"target": "Stable"})
	if !strings.Contains(got, "go:internal/sorting → go:adapters/memory") || strings.Contains(got, "go:internal/graph → go:adapters/reflectx") {
		t.Fatalf("only the new violation is news:\n%s", got)
	}

	// Tools that list violations themselves only mark them told.
	write(t, root, "injection/extra.go", "package injection\n\nimport \"github.com/smarty/injection/adapters/memory\"\n\nvar extra memory.Store\n")
	if err := server.live.Refresh(); err != nil {
		t.Fatal(err)
	}

	if got := callText(t, server, "changes", nil); strings.Contains(got, "you have not been told about") {
		t.Fatalf("changes reports violations itself:\n%s", got)
	}

	if got := callText(t, server, "find", map[string]string{"query": "Order"}); strings.Contains(got, "layer violation") {
		t.Fatalf("a violation listed by changes is told:\n%s", got)
	}
}

func callText(t *testing.T, server *Server, name string, arguments map[string]string) string {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	result, failure := server.call(params)
	if failure != nil {
		t.Fatalf("%s: %+v", name, failure)
	}

	content := result.(map[string]any)["content"].([]any)
	return content[0].(map[string]string)["text"]
}

// fixture copies the PR #142 fixture so a test may edit it.
func fixture(t *testing.T) string {
	t.Helper()
	from, root := "../golang/testdata/injection", t.TempDir()
	err := filepath.WalkDir(from, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, _ := filepath.Rel(from, current)
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(root, relative), 0o755)
		}

		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(root, relative), data, 0o644)
	})

	if err != nil {
		t.Fatal(err)
	}

	return root
}

func serve(t *testing.T, root string) (*Server, *app.Service) {
	t.Helper()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}

	service := app.NewService(root, filesystem.NewConfig(root), golang.NewExtractor(), []app.Dialect{golang.NewDialect()}, gitvcs.New(root), filesystem.NewWorkspace(root), htmlmap.New(), filesystem.NewAgentConfig(root), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	live := app.NewLive(service, filesystem.NewWatcher(root), nil)
	if err := live.Refresh(); err != nil {
		t.Fatal(err)
	}

	return New(live, "test", "http://127.0.0.1:0", false), service
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
