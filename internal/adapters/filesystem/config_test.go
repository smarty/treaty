package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

func TestLoadArchitectures(t *testing.T) {
	for _, c := range []struct {
		name  string
		yaml  string
		style string
		want  []string
	}{
		{"hexagonal by default", `
layers:
  adapter:
    driven: ["db/**"]
    driving: ["web/**"]
  composition: ["cmd/**"]
  domain: ["core/**"]
  application: ["app/**"]
mutation:
  scope: blast_radius
`, rules.StyleHexagonal, []string{"domain", "application", "adapter/driving", "adapter/driven"}},
		{"clean in any order", `
architecture: clean
layers:
  use_cases: ["app/**"]
  frameworks: ["web/**"]
  entities: ["core/**"]
`, rules.StyleClean, []string{"entities", "use_cases", "frameworks"}},
		{"layered top to bottom", `
architecture: layered
composition: ["cmd/**"]
layers:
  presentation: ["web/**"]
  business: ["service/**"]
  data: ["store/**"]
`, rules.StyleLayered, []string{"data", "business", "presentation"}},
		{"slices with layers", `
architecture: slices
shared: ["platform/**"]
slices: ["features/*"]
layers:
  api: ["."]
  store: ["store/**"]
`, rules.StyleSlices, []string{"store", "api"}},
		{"modular", `
architecture: modular
contexts: ["internal/*"]
public: [".", "api/**"]
`, rules.StyleModular, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			config, found, err := NewConfig(root).Load()
			if err != nil || !found {
				t.Fatalf("found %t, err %v", found, err)
			}

			if config.Architecture.Style != c.style {
				t.Errorf("style %q", config.Architecture.Style)
			}

			var got []string
			for _, layer := range config.Architecture.Layers {
				name := layer.Name
				if layer.Side != "" {
					name += "/" + layer.Side
				}

				got = append(got, name)
			}

			if !slices.Equal(got, c.want) {
				t.Errorf("layers %v, want %v", got, c.want)
			}

			if !slices.Contains(config.FailOn, rules.FindingCycle) {
				t.Errorf("fail_on %v should include cycles by default", config.FailOn)
			}
		})
	}
}

func TestLoadRejectsBadArchitectures(t *testing.T) {
	for _, text := range []string{
		"architecture: onion\n",
		"architecture: modular\nslices: [\"internal/*\"]\n",
		"architecture: slices\ncontexts: [\"internal/*\"]\n",
		"architecture: clean\nlayers:\n  services: [\"x\"]\n",
		"layers: [\"x\"]\n",
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := NewConfig(root).Load(); !errors.Is(err, rules.ErrArchitecture) {
			t.Errorf("%q: got %v", text, err)
		}
	}
}

func TestLoadedHexagonalPlacesAdapters(t *testing.T) {
	root := t.TempDir()
	text := "layers:\n  composition: [\"cmd/**\"]\n  adapter:\n    driving: [\"web/**\"]\n"
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	config, _, err := NewConfig(root).Load()
	if err != nil {
		t.Fatal(err)
	}

	if place := config.Architecture.Resolve("web/api"); place.Layer != graph.LayerAdapter || place.Side != graph.SideDriving {
		t.Errorf("web/api: %+v", place)
	}

	if place := config.Architecture.Resolve("cmd/tool"); place.Layer != graph.LayerComposition {
		t.Errorf("cmd/tool: %+v", place)
	}
}
