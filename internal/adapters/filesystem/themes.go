package filesystem

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/app"
)

// ThemeManifest records which files in the theme folder are Treaty's
// defaults, so that Install never touches a person's own themes and can
// remove a default that no longer ships.
const ThemeManifest = ".defaults.json"

var (
	//go:embed themes/*.json
	defaultThemes embed.FS

	// DefaultThemeOrder lists the standard and accessibility themes in the
	// order people see them; the style themes follow by name.
	DefaultThemeOrder = []string{"light", "dark", "color-blind-light", "color-blind-dark", "high-contrast-light", "high-contrast-dark"}

	// ThemeGroups lists the menu's groups in order.
	ThemeGroups = []string{"standard", "accessibility", "style", "yours"}

	ErrTheme = errors.New("invalid theme")

	colorValue = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|(rgb|rgba|hsl|hsla)\([0-9a-zA-Z.,%/ ]+\))$`)
	tokenName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Themes reads the map's color themes from a folder, ~/.treaty/themes, and
// installs Treaty's defaults there.
type Themes struct {
	dir string
}

// themeFile is the JSON shape of one theme.
type themeFile struct {
	Warning string            `json:"warning,omitempty"`
	Name    string            `json:"name"`
	Base    string            `json:"base"`
	Group   string            `json:"group,omitempty"`
	Colors  map[string]string `json:"colors"`
}

// themeManifest is the JSON shape of ThemeManifest.
type themeManifest struct {
	Warning  string   `json:"warning"`
	Defaults []string `json:"defaults"`
}

// NewThemes creates a theme source for a folder.
//
// Parameters:
//   - dir: the theme folder; empty means only the built-in defaults.
//
// Returns:
//   - result: the theme source.
func NewThemes(dir string) *Themes {
	return &Themes{dir: dir}
}

// Install writes every default theme into the folder, replacing earlier
// copies, removes defaults an earlier version wrote that no longer ship,
// and records the defaults in ThemeManifest. Other files are never touched.
//
// Returns:
//   - err: the folder or a file could not be written.
func (this *Themes) Install() error {
	if this.dir == "" {
		return nil
	}

	if err := os.MkdirAll(this.dir, 0o755); err != nil {
		return err
	}

	names, err := defaultThemeNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		data, err := defaultThemes.ReadFile(path.Join("themes", name))
		if err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(this.dir, name), data, 0o644); err != nil {
			return err
		}
	}

	for _, stale := range this.installed() {
		if !slices.Contains(names, stale) && filepath.Base(stale) == stale {
			_ = os.Remove(filepath.Join(this.dir, stale))
		}
	}

	manifest, err := json.MarshalIndent(themeManifest{
		Warning:  "Treaty keeps this file. It lists the default themes, which Treaty rewrites every time the server starts; do not edit it.",
		Defaults: names,
	}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(this.dir, ThemeManifest), append(manifest, '\n'), 0o644)
}

// Themes lists the defaults, then the person's own themes by name. A default
// comes from its file in the folder when there is one, so a person can try a
// change before it is overwritten, and from the built-in copy otherwise.
//
// Returns:
//   - result: every theme that could be read.
//   - err: the themes that could not be read, and why.
//
// Errors:
//   - ErrTheme: a theme file is not valid JSON, has no colors, names an
//     unknown base, or holds a token name or color value the map cannot use.
func (this *Themes) Themes() (result []app.Theme, err error) {
	names, err := defaultThemeNames()
	if err != nil {
		return nil, err
	}

	byID, defaultGroup := map[string]app.Theme{}, map[string]string{}
	for _, name := range names {
		data, err := defaultThemes.ReadFile(path.Join("themes", name))
		if err != nil {
			return nil, err
		}

		theme, err := parseTheme(name, data)
		if err != nil {
			return nil, err
		}

		theme.Default = true
		byID[theme.ID] = theme
		defaultGroup[theme.ID] = theme.Group
	}

	var problems []error
	entries, readErr := os.ReadDir(this.dir)
	if this.dir != "" && readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		problems = append(problems, readErr)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == ThemeManifest || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(this.dir, name))
		if err == nil {
			var theme app.Theme
			if theme, err = parseTheme(name, data); err == nil {
				// A default keeps its group; anything else is the person's own.
				theme.Default = slices.Contains(names, name)
				theme.Group = "yours"
				if group, ok := defaultGroup[theme.ID]; ok && theme.Default {
					theme.Group = group
				}

				byID[theme.ID] = theme
				continue
			}
		}

		problems = append(problems, fmt.Errorf("theme %s: %w", filepath.Join(this.dir, name), err))
	}

	for _, theme := range byID {
		result = append(result, theme)
	}

	order := func(theme app.Theme) (group, index int) {
		index = slices.Index(DefaultThemeOrder, theme.ID)
		if index < 0 || !theme.Default {
			index = len(DefaultThemeOrder)
		}

		return slices.Index(ThemeGroups, theme.Group), index
	}

	sort.Slice(result, func(i, j int) bool {
		gi, ii := order(result[i])
		gj, ij := order(result[j])
		if gi != gj {
			return gi < gj
		}

		if ii != ij {
			return ii < ij
		}

		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, errors.Join(problems...)
}

// installed reads the defaults an earlier Install recorded.
func (this *Themes) installed() []string {
	data, err := os.ReadFile(filepath.Join(this.dir, ThemeManifest))
	if err != nil {
		return nil
	}

	var manifest themeManifest
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}

	return manifest.Defaults
}

func defaultThemeNames() ([]string, error) {
	entries, err := defaultThemes.ReadDir("themes")
	if err != nil {
		return nil, err
	}

	var result []string
	for _, entry := range entries {
		result = append(result, entry.Name())
	}

	return result, nil
}

// parseTheme reads one theme file; its id is the file name without .json.
func parseTheme(name string, data []byte) (app.Theme, error) {
	var file themeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return app.Theme{}, fmt.Errorf("%w: %v", ErrTheme, err)
	}

	id := strings.TrimSuffix(name, ".json")
	theme := app.Theme{ID: id, Name: file.Name, Base: file.Base, Group: file.Group, Colors: file.Colors}
	if theme.Name == "" {
		theme.Name = id
	}

	if theme.Base == "" {
		theme.Base = "light"
	}

	if theme.Base != "light" && theme.Base != "dark" {
		return app.Theme{}, fmt.Errorf("%w: base must be light or dark, not %q", ErrTheme, theme.Base)
	}

	if len(theme.Colors) == 0 {
		return app.Theme{}, fmt.Errorf("%w: it has no colors", ErrTheme)
	}

	for token, value := range theme.Colors {
		if !tokenName.MatchString(token) {
			return app.Theme{}, fmt.Errorf("%w: %q is not a token name", ErrTheme, token)
		}

		if !colorValue.MatchString(strings.TrimSpace(value)) {
			return app.Theme{}, fmt.Errorf("%w: %s: %q is not a color", ErrTheme, token, value)
		}
	}

	return theme, nil
}
