package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/smarty/treaty/internal/app"
)

var treatyServer = app.MCPServer{Command: "treaty", Args: []string{"mcp"}}

func TestRegisterCreatesTheFile(t *testing.T) {
	root := t.TempDir()
	result, err := NewAgentConfig(root).Register("treaty", treatyServer, false)
	if err != nil || result.Outcome != app.RegistrationCreated {
		t.Fatalf("%+v %v", result, err)
	}

	want := "{\n  \"mcpServers\": {\n    \"treaty\": {\n      \"command\": \"treaty\",\n      \"args\": [\n        \"mcp\"\n      ]\n    }\n  }\n}\n"
	if got := read(t, root); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRegisterKeepsEverythingElse(t *testing.T) {
	root := t.TempDir()
	existing := "{\n    \"mcpServers\": {\n        \"zeta\": {\"command\": \"zeta\"},\n        \"alpha\": {\"command\": \"alpha\", \"env\": {\"A\": \"1\"}}\n    },\n    \"other\": true\n}\n"
	writeFile(t, root, existing)
	result, err := NewAgentConfig(root).Register("treaty", treatyServer, false)
	if err != nil || result.Outcome != app.RegistrationAdded {
		t.Fatalf("%+v %v", result, err)
	}

	want := "{\n    \"mcpServers\": {\n        \"zeta\": {\n            \"command\": \"zeta\"\n        },\n        \"alpha\": {\n            \"command\": \"alpha\",\n            \"env\": {\n                \"A\": \"1\"\n            }\n        },\n        \"treaty\": {\n            \"command\": \"treaty\",\n            \"args\": [\n                \"mcp\"\n            ]\n        }\n    },\n    \"other\": true\n}\n"
	if got := read(t, root); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	// Running it again changes nothing, even when the entry has extra settings.
	writeFile(t, root, `{"mcpServers": {"treaty": {"type": "stdio", "command": "treaty", "args": ["mcp"]}}}`)
	if result, err := NewAgentConfig(root).Register("treaty", treatyServer, false); err != nil || result.Outcome != app.RegistrationUnchanged {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRegisterRefusesADifferentEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, `{"mcpServers": {"treaty": {"command": "/opt/old/treaty", "args": ["mcp"]}}}`)
	if _, err := NewAgentConfig(root).Register("treaty", treatyServer, false); !errors.Is(err, ErrRegistered) {
		t.Fatalf("want ErrRegistered, got %v", err)
	}

	if result, err := NewAgentConfig(root).Register("treaty", treatyServer, true); err != nil || result.Outcome != app.RegistrationReplaced {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRegisterLeavesAnInvalidFileAlone(t *testing.T) {
	root := t.TempDir()
	for _, text := range []string{`{"mcpServers": `, `[]`, `{"mcpServers": []}`, `{} {}`} {
		writeFile(t, root, text)
		if _, err := NewAgentConfig(root).Register("treaty", treatyServer, false); !errors.Is(err, ErrMCPFile) {
			t.Errorf("%s: want ErrMCPFile, got %v", text, err)
		}

		if got := read(t, root); got != text {
			t.Errorf("%s: the file changed to %s", text, got)
		}
	}
}

func read(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, MCPFile))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func writeFile(t *testing.T, root, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, MCPFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
