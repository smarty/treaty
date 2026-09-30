package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
)

// maxThemeName bounds a theme choice; theme ids are file names.
const maxThemeName = 128

var ErrPreferences = errors.New("invalid preferences")

// Preferences reads the person's choices on the map.
//
// Returns:
//   - result: the preferences, empty when none have been saved.
//   - err: they could not be read.
func (this *Live) Preferences() (result Preferences, err error) {
	this.saving.Lock()
	defer this.saving.Unlock()
	return this.service.preferences.Load()
}

// SavePreferences merges an update into the person's choices and saves
// them: each field the update sets replaces the saved one, and the others
// stay as they were.
//
// Parameters:
//   - update: the fields to change.
//
// Returns:
//   - result: the preferences after the update.
//   - err: the update is invalid, or the preferences could not be saved.
//
// Errors:
//   - ErrPreferences: the theme is too long or holds control characters, or
//     the layout is not a JSON object.
func (this *Live) SavePreferences(update Preferences) (result Preferences, err error) {
	if len(update.Theme) > maxThemeName {
		return Preferences{}, fmt.Errorf("%w: theme name longer than %d characters", ErrPreferences, maxThemeName)
	}

	for _, r := range update.Theme {
		if unicode.IsControl(r) {
			return Preferences{}, fmt.Errorf("%w: theme name holds control characters", ErrPreferences)
		}
	}

	if len(update.Layout) > 0 && (!json.Valid(update.Layout) || !bytes.HasPrefix(bytes.TrimSpace(update.Layout), []byte("{"))) {
		return Preferences{}, fmt.Errorf("%w: layout must be a JSON object", ErrPreferences)
	}

	this.saving.Lock()
	defer this.saving.Unlock()
	result, err = this.service.preferences.Load()
	if err != nil {
		result = Preferences{}
	}

	if update.Theme != "" {
		result.Theme = update.Theme
	}

	if update.Follow != nil {
		result.Follow = update.Follow
	}

	if len(update.Layout) > 0 {
		result.Layout = update.Layout
	}

	return result, this.service.preferences.Save(result)
}
