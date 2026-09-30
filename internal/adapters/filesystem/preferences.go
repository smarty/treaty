package filesystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/smarty/treaty/internal/app"
)

// Preferences keeps a person's choices on the map in one JSON file,
// ~/.treaty/settings.json, shared by every repository and session.
type Preferences struct {
	path string
}

// NewPreferences creates a preference store for a file.
//
// Parameters:
//   - path: the settings file; empty keeps nothing.
//
// Returns:
//   - result: the preference store.
func NewPreferences(path string) *Preferences {
	return &Preferences{path: path}
}

// Load reads the preferences.
//
// Returns:
//   - result: the preferences, empty when the file does not exist.
//   - err: the file could not be read or parsed.
func (this *Preferences) Load() (result app.Preferences, err error) {
	if this.path == "" {
		return app.Preferences{}, nil
	}

	data, err := os.ReadFile(this.path)
	if errors.Is(err, fs.ErrNotExist) {
		return app.Preferences{}, nil
	}

	if err != nil {
		return app.Preferences{}, err
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return app.Preferences{}, fmt.Errorf("%s: %w", this.path, err)
	}

	return result, nil
}

// Save replaces the preferences, writing a temporary file and renaming it
// into place, so that two servers saving at once never leave a torn file.
//
// Parameters:
//   - preferences: the preferences to keep.
//
// Returns:
//   - err: the file could not be written.
func (this *Preferences) Save(preferences app.Preferences) error {
	if this.path == "" {
		return nil
	}

	data, err := json.MarshalIndent(preferences, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(this.path), 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(this.path), ".settings-*.json")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}

	if err := temp.Close(); err != nil {
		return err
	}

	return os.Rename(temp.Name(), this.path)
}
