package app

import (
	"errors"
	"fmt"
	"slices"
	"unicode"

	"github.com/smarty/treaty/internal/rules"
)

// maxRegionName bounds a region's label and the name given to it.
const maxRegionName = 128

var ErrMapSettings = errors.New("invalid map settings")

// MapSettings reads the person's choices on this repository's map.
//
// Returns:
//   - result: the choices, empty when none have been saved.
//   - err: they could not be read.
func (this *Live) MapSettings() (result MapSettings, err error) {
	this.settings.Lock()
	defer this.settings.Unlock()
	return this.service.workspace.MapSettings()
}

// SaveMapSettings merges an update into the person's choices on this
// repository's map and saves them: each field the update sets replaces the
// saved one, and each region name it sets replaces that region's, or, when
// empty, forgets it, so the region takes its label again.
//
// Parameters:
//   - update: the fields and region names to change.
//
// Returns:
//   - result: the choices after the update.
//   - err: the update is invalid, or the choices could not be saved.
//
// Errors:
//   - ErrMapSettings: a region name is for an unknown architecture, or a
//     label or name is too long or holds control characters.
func (this *Live) SaveMapSettings(update MapSettings) (result MapSettings, err error) {
	for architecture, names := range update.Names {
		if !slices.Contains(rules.Styles, architecture) {
			return MapSettings{}, fmt.Errorf("%w: unknown architecture %q", ErrMapSettings, architecture)
		}

		for label, name := range names {
			if err := regionText(label); err != nil {
				return MapSettings{}, err
			}

			if err := regionText(name); err != nil {
				return MapSettings{}, err
			}
		}
	}

	this.settings.Lock()
	defer this.settings.Unlock()
	result, err = this.service.workspace.MapSettings()
	if err != nil {
		result = MapSettings{}
	}

	if update.Follow != nil {
		result.Follow = update.Follow
	}

	if update.Internals != nil {
		result.Internals = update.Internals
	}

	for architecture, names := range update.Names {
		if result.Names == nil {
			result.Names = map[string]map[string]string{}
		}

		if result.Names[architecture] == nil {
			result.Names[architecture] = map[string]string{}
		}

		for label, name := range names {
			if name == "" || name == label {
				delete(result.Names[architecture], label)
			} else {
				result.Names[architecture][label] = name
			}
		}

		if len(result.Names[architecture]) == 0 {
			delete(result.Names, architecture)
		}
	}

	return result, this.service.workspace.SaveMapSettings(result)
}

// regionText checks a region's label or the name given to it.
func regionText(text string) error {
	if len(text) > maxRegionName {
		return fmt.Errorf("%w: region name longer than %d characters", ErrMapSettings, maxRegionName)
	}

	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: region name holds control characters", ErrMapSettings)
		}
	}

	return nil
}
