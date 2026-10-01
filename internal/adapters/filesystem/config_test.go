package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		{"none when nothing is declared", `
rules:
  warn_on: [unclassified]
`, rules.StyleNone, nil},
		{"none declared", `
architecture: none
`, rules.StyleNone, nil},
		{"hexagonal when layers have no architecture key", `
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
		"architecture: none\nlayers:\n  data: [\"x\"]\n",
		"architecture: none\ncomposition: [\"cmd/**\"]\n",
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

func TestLoadWithoutConfigIsNone(t *testing.T) {
	config, found, err := NewConfig(t.TempDir()).Load()
	if err != nil || found {
		t.Fatalf("found %t, err %v", found, err)
	}

	if config.Architecture.Style != rules.StyleNone || config.Architecture.Resolve("anything").Layer != graph.LayerNone {
		t.Fatalf("no treaty.yaml declares no architecture: %+v", config.Architecture)
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

func TestPlaceEditsTreatyYAML(t *testing.T) {
	const original = `# Our layers.
architecture: hexagonal
layers:
  composition: ["cmd/**"]
  domain: ["internal/core/**"] # the pure core
  application: ["internal/app/**"]
  adapter:
    driving: ["internal/web/**"]
rules:
  fail_on: [breaking]
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	config := NewConfig(root)
	place := func(path string, placement rules.Placement) string {
		t.Helper()
		if _, err := config.Place(path, placement); err != nil {
			t.Fatalf("Place(%s, %+v): %v", path, placement, err)
		}

		loaded, _, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}

		if got := loaded.Architecture.Resolve(path); got != placement {
			t.Fatalf("%s resolves to %+v, want %+v", path, got, placement)
		}

		data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
		return string(data)
	}

	text := place("internal/web/admin", rules.Placement{Layer: graph.LayerAdapter, Side: graph.SideDriven})
	for _, want := range []string{"# Our layers.", "# the pure core", `driven: ["internal/web/admin"]`, "fail_on: [breaking]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the edit must keep comments and add the module to a new side list (%q):\n%s", want, text)
		}
	}

	text = place("internal/web/admin", rules.Placement{Layer: graph.LayerAdapter, Side: graph.SideDriving})
	if strings.Contains(text, `"internal/web/admin"`) {
		t.Fatalf("moving back under its wildcard must only remove the exact path:\n%s", text)
	}

	text = place("cmd/tool", rules.Placement{Layer: graph.LayerApplication})
	if !strings.Contains(text, `application: ["internal/app/**", "cmd/tool"]`) {
		t.Fatalf("the module joins the target's list in its style:\n%s", text)
	}

	place("cmd/tool", rules.Placement{Layer: graph.LayerComposition})
	if _, err := NewConfig(t.TempDir()).Place("x", rules.Placement{Layer: graph.LayerDomain}); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("placing without treaty.yaml must fail with ErrNoConfig: %v", err)
	}
}

func TestPlaceInLayeredComposition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte("architecture: layered\nlayers:\n  web:\n    - web/**\n  store:\n    - store/**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	config := NewConfig(root)
	for _, placement := range []rules.Placement{{Layer: graph.LayerComposition}, {Layer: "store"}, {Layer: "web"}} {
		if _, err := config.Place("web/main", placement); err != nil {
			t.Fatal(err)
		}

		loaded, _, _ := config.Load()
		if got := loaded.Architecture.Resolve("web/main"); got != placement {
			t.Fatalf("web/main resolves to %+v, want %+v", got, placement)
		}
	}

	data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
	if text := string(data); !strings.Contains(text, "composition: []\nlayers:") || !strings.Contains(text, "    - store/**\n") {
		t.Fatalf("composition goes above layers and block lists stay block lists:\n%s", text)
	}
}

func TestDesignsBeforeTheRename(t *testing.T) {
	root := t.TempDir()
	workspace := NewWorkspace(root)
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(root, Directory, "designs", "old.cml")
	if err := os.WriteFile(old, []byte("cml 1\ndesign \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := workspace.CreateDesign("fresh", "autopen 1\n"); err != nil {
		t.Fatal(err)
	}

	if names, _ := workspace.Designs(); !slices.Equal(names, []string{"fresh", "old"}) {
		t.Fatalf("designs: %v", names)
	}

	if text, err := workspace.ReadDesign("old"); err != nil || !strings.HasPrefix(text, "cml 1") {
		t.Fatalf("an old .cml design still reads: %q %v", text, err)
	}

	if _, err := workspace.CreateDesign("old", "autopen 1\n"); !errors.Is(err, ErrDesignExists) {
		t.Fatalf("an old .cml design blocks creating its name: %v", err)
	}

	path, err := workspace.SaveDesign("old", "autopen 1\n")
	if err != nil || filepath.Ext(path) != DesignExtension {
		t.Fatalf("saving writes .pen: %s %v", path, err)
	}

	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("saving replaces the old .cml file: %v", err)
	}
}
