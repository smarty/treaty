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

const mapSettingsFile = "map.json"

// MapSettings reads .treaty/map.json: the person's choices on this
// repository's map.
//
// Returns:
//   - result: the choices; empty when none have been saved.
//   - err: the file could not be read or parsed.
func (this *Workspace) MapSettings() (result app.MapSettings, err error) {
	data, err := os.ReadFile(filepath.Join(this.root, Directory, mapSettingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return app.MapSettings{}, nil
	}

	if err != nil {
		return app.MapSettings{}, err
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return app.MapSettings{}, fmt.Errorf("%s: %w", mapSettingsFile, err)
	}

	return result, nil
}

// SaveMapSettings replaces .treaty/map.json, which the workspace's
// .gitignore keeps out of version control.
//
// Parameters:
//   - settings: the choices on the map.
//
// Returns:
//   - err: the file could not be written.
func (this *Workspace) SaveMapSettings(settings app.MapSettings) error {
	if err := this.Init(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(this.root, Directory, mapSettingsFile)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return err
	}

	return os.Rename(temporary, path)
}
