package filesystem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/smarty/treaty/internal/app"
)

func TestPreferencesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".treaty", "settings.json")
	store := NewPreferences(path)
	if saved, err := store.Load(); err != nil || saved.Theme != "" || saved.Layout != nil {
		t.Fatalf("nothing saved yet: %+v %v", saved, err)
	}

	follow := true
	want := app.Preferences{Theme: "neon-rain", Follow: &follow, Layout: json.RawMessage(`{"center":{"tabs":["map"]}}`)}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := NewPreferences(path).Load()
	var layout bytes.Buffer
	if err == nil {
		err = json.Compact(&layout, got.Layout)
	}

	if err != nil || got.Theme != "neon-rain" || got.Follow == nil || !*got.Follow || layout.String() != `{"center":{"tabs":["map"]}}` {
		t.Fatalf("got %+v %s %v", got, layout.String(), err)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("only settings.json should remain, not a temporary file: %v", entries)
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load(); err == nil {
		t.Error("a broken settings file must be reported")
	}

	if err := NewPreferences("").Save(want); err != nil {
		t.Errorf("with no home, saving does nothing: %v", err)
	}
}
