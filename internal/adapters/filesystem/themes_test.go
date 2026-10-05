package filesystem

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestThemesInstallAndDetect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "themes")
	themes := NewThemes(dir)

	// Before any server has started, the built-in defaults are there.
	list, err := themes.Themes()
	if err != nil || len(list) != 21 || !list[0].Default {
		t.Fatalf("built-in themes: %d %v", len(list), err)
	}

	var first []string
	for _, theme := range list[:7] {
		first = append(first, theme.ID+":"+theme.Group)
	}

	want := "light:standard,dark:standard,color-blind-light:accessibility,color-blind-dark:accessibility,high-contrast-light:accessibility,high-contrast-dark:accessibility,blossom:style"
	if strings.Join(first, ",") != want {
		t.Fatalf("order:\n%s\nwant\n%s", strings.Join(first, ","), want)
	}

	// A default an earlier version shipped, and a person's own theme.
	writeTheme(t, dir, ThemeManifest, `{"defaults": ["light.json", "retired.json"]}`)
	writeTheme(t, dir, "retired.json", `{"name": "Retired", "colors": {"bg": "#000000"}}`)
	writeTheme(t, dir, "aurora.json", `{"name": "Aurora", "base": "dark", "colors": {"bg": "#0b1020", "ink": "rgb(230, 235, 255)"}}`)
	writeTheme(t, dir, "light.json", `{"name": "Edited", "colors": {"bg": "#123456"}}`)
	if err := themes.Install(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"light.json", "dark.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.Contains(string(data), "any change you make here will be lost") {
			t.Fatalf("%s must be a default theme with its warning: %v\n%s", name, err, data)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "retired.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a default that no longer ships must be removed: %v", err)
	}

	if data, _ := os.ReadFile(filepath.Join(dir, "aurora.json")); !strings.Contains(string(data), "Aurora") {
		t.Errorf("a person's own theme must be left alone:\n%s", data)
	}

	var manifest themeManifest
	data, _ := os.ReadFile(filepath.Join(dir, ThemeManifest))
	if json.Unmarshal(data, &manifest) != nil || len(manifest.Defaults) != 21 || !slices.Contains(manifest.Defaults, "light.json") || slices.Contains(manifest.Defaults, "retired.json") {
		t.Errorf("manifest: %s", data)
	}

	// A new theme is detected, and a broken one is reported without hiding
	// the rest.
	writeTheme(t, dir, "broken.json", `{"name": "Broken", "colors": {"bg": "url(evil)"}}`)
	list, err = themes.Themes()
	if !errors.Is(err, ErrTheme) || !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("the broken theme must be reported: %v", err)
	}

	// Any name that is not a default's is the person's own, listed last.
	writeTheme(t, dir, "sunrise.json", `{"name": "Sunrise", "group": "style", "colors": {"bg": "#fff7e6"}}`)
	list, _ = themes.Themes()
	if len(list) != 23 {
		t.Fatalf("themes: %d", len(list))
	}

	var yours []string
	for _, theme := range list[21:] {
		if theme.Default || theme.Group != "yours" {
			t.Errorf("a person's theme must be theirs, whatever group it claims: %+v", theme)
		}

		yours = append(yours, theme.ID)
	}

	if strings.Join(yours, ",") != "aurora,sunrise" {
		t.Errorf("a person's themes come last, by name: %v", yours)
	}
}

func TestParseThemeRejectsUnusableTokens(t *testing.T) {
	for _, text := range []string{
		`{"colors": {}}`,
		`{"base": "sepia", "colors": {"bg": "#fff"}}`,
		`{"colors": {"Bad Name": "#fff"}}`,
		`{"colors": {"bg": "#fff; background: url(x)"}}`,
		`not json`,
	} {
		if _, err := parseTheme("x.json", []byte(text)); !errors.Is(err, ErrTheme) {
			t.Errorf("%s: got %v", text, err)
		}
	}
}

func writeTheme(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
