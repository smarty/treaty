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

const positionsFile = "positions.json"

// Positions reads .treaty/positions.json: where the person put modules on
// this repository's map.
//
// Returns:
//   - result: each architecture's module positions; empty when nothing has
//     been moved.
//   - err: the file could not be read or parsed.
func (this *Workspace) Positions() (result app.Positions, err error) {
	data, err := os.ReadFile(filepath.Join(this.root, Directory, positionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return app.Positions{}, nil
	}

	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", positionsFile, err)
	}

	if result == nil {
		result = app.Positions{}
	}

	return result, nil
}

// SavePositions replaces .treaty/positions.json, which the workspace's
// .gitignore keeps out of version control.
//
// Parameters:
//   - positions: each architecture's module positions.
//
// Returns:
//   - err: the file could not be written.
func (this *Workspace) SavePositions(positions app.Positions) error {
	if err := this.Init(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(positions, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(this.root, Directory, positionsFile)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return err
	}

	return os.Rename(temporary, path)
}
