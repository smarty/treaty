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
version: 1
rules:
  warn_on: [unclassified]
`, rules.StyleNone, nil},
		{"none declared", `
architecture: none
`, rules.StyleNone, nil},
		{"hexagonal in any order", `
architecture: hexagonal
layers:
  adapter:
    driven: ["db/**"]
    driving: ["web/**"]
  composition: ["cmd/**"]
  domain: ["core/**"]
  application: ["app/**"]
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
		{"vertical with layers", `
architecture: vertical
shared: ["platform/**"]
boundaries:
  orders: ["**/orders/**"]
  billing: ["web/billing/**", "store/billing/**"]
layers:
  api: ["web/**"]
  store: ["store/**"]
`, rules.StyleVertical, []string{"store", "api"}},
		{"modular", `
architecture: modular
boundaries:
  orders: ["internal/orders/**"]
public: ["**/api/**"]
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
		"layers:\n  data: [\"x\"]\n",
		"architecture: vertical\n",
		"architecture: vertical\nboundaries: [\"internal/*\"]\n",
		"architecture: modular\nboundaries:\n  orders: []\n",
		"architecture: layered\nlayers:\n  web: [\"web/**\"]\nboundaries:\n  orders: [\"x/**\"]\n",
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
	text := "architecture: hexagonal\nlayers:\n  composition: [\"cmd/**\"]\n  adapter:\n    driving: [\"web/**\"]\n"
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
  fail_on: [breaking_changes]
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

		if got := loaded.Architecture.Resolve(path); !got.Same(placement) {
			t.Fatalf("%s resolves to %+v, want %+v", path, got, placement)
		}

		data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
		return string(data)
	}

	text := place("internal/web/admin", rules.Placement{Layer: graph.LayerAdapter, Side: graph.SideDriven})
	for _, want := range []string{"# Our layers.", "# the pure core", `driven: ["internal/web/admin"]`, "fail_on: [breaking_changes]"} {
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
		if got := loaded.Architecture.Resolve("web/main"); !got.Same(placement) {
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

func TestLoadReadsVersions(t *testing.T) {
	for _, c := range []struct {
		name        string
		yaml        string
		unversioned bool
	}{
		{"no version line is version 0", "architecture: none\n", true},
		{"version 0 said outright", "version: 0\narchitecture: none\n", false},
		{"the current version", "version: 1\narchitecture: none\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			config, _, err := NewConfig(root).Load()
			if err != nil {
				t.Fatal(err)
			}

			if config.Unversioned != c.unversioned {
				t.Errorf("unversioned %t, want %t", config.Unversioned, c.unversioned)
			}
		})
	}
}

func TestLoadRejectsWhatItCannotRead(t *testing.T) {
	for _, c := range []struct {
		name string
		yaml string
		want error
		says string
	}{
		{"a newer version", "version: 2\narchitecture: none\n", ErrConfigVersion, "line 1: unsupported treaty.yaml version: the file is version 2 and this treaty reads up to version 1; upgrade treaty"},
		{"a newer version with keys from it", "version: 2\nfuture: true\n", ErrConfigVersion, "upgrade treaty"},
		{"a version that is not a number", "version: \"1\"\n", ErrConfigVersion, "version must be a whole number"},
		{"a negative version", "version: -1\n", ErrConfigVersion, "version must be a whole number"},
		{"a misspelled key", "version: 1\narchitecure: clean\n", ErrUnknownKey, `line 2: unknown key "architecure"; did you mean "architecture"?`},
		{"a key from a newer treaty", "version: 1\nmutation:\n  scope: all\n", ErrUnknownKey, `unknown key "mutation"; a newer treaty may know it`},
		{"a misspelled rules key", "rules:\n  fail_one: [breaking_changes]\n", ErrUnknownKey, `line 2: unknown key "fail_one" under rules; did you mean "fail_on"?`},
		{"a misspelled adapter side", "layers:\n  adapter:\n    drivng: [\"web/**\"]\n", ErrUnknownKey, `unknown key "drivng" under layers.adapter; did you mean "driving"?`},
		{"a misspelled rule", "rules:\n  fail_on: [breaking_changes, layer_violations]\n", ErrUnknownRule, `line 2: fail_on: unknown rule "layer_violations"; did you mean "layer_violation"?`},
		{"a finding that is not a rule", "rules:\n  fail_on: [new_contract]\n", ErrUnknownRule, "fail_on takes breaking_changes, layer_violation, dependency_cycles, unclassified, ambiguous_boundaries"},
		{"an unknown warning", "rules:\n  warn_on: [everything]\n", ErrUnknownRule, "warn_on takes breaking_changes"},
		{"an old rule name", "version: 1\nrules:\n  fail_on: [breaking]\n", ErrUnknownRule, `line 3: fail_on: unknown rule "breaking"; since version 1 it is "breaking_changes"`},
		{"a rule name from before version 1", "rules:\n  fail_on: [cycle]\n", ErrUnknownRule, `line 2: fail_on: unknown rule "cycle"; since version 1 it is "dependency_cycles"`},
		{"slices from before boundaries", "architecture: modular\nslices: [\"internal/*\"]\n", ErrUnknownKey, `unknown key "slices"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			_, found, err := NewConfig(root).Load()
			if !found || !errors.Is(err, c.want) {
				t.Fatalf("found %t, err %v, want %v", found, err, c.want)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q should say %q", err, c.says)
			}
		})
	}
}

func TestLayerNamesAreNotKeys(t *testing.T) {
	root := t.TempDir()
	text := "version: 1\narchitecture: layered\nlayers:\n  anything_goes: [\"web/**\"]\n  adapter: [\"db/**\"]\n"
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewConfig(root).Load(); err != nil {
		t.Fatalf("a layered architecture names its own layers: %v", err)
	}
}

func TestRuleNames(t *testing.T) {
	for _, c := range []struct {
		name string
		yaml string
		fail []string
		warn []string
	}{
		{"defaults", "version: 1\n", []string{rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingCycle}, []string{rules.FindingUnclassified, rules.FindingAmbiguous}},
		{"version 1 names", "version: 1\nrules:\n  fail_on: [breaking_changes, dependency_cycles, ambiguous_boundaries]\n  warn_on: [unclassified, layer_violation]\n",
			[]string{rules.FindingBreaking, rules.FindingCycle, rules.FindingAmbiguous}, []string{rules.FindingUnclassified, rules.FindingLayerViolation}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			config, _, err := NewConfig(root).Load()
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(config.FailOn, c.fail) || !slices.Equal(config.WarnOn, c.warn) {
				t.Errorf("fail_on %v, warn_on %v; want %v and %v", config.FailOn, config.WarnOn, c.fail, c.warn)
			}
		})
	}
}

func TestPlaceInBoundaries(t *testing.T) {
	const original = `version: 1
architecture: vertical
composition: ["cmd/**"]
shared: ["lib/**"] # used by every boundary
boundaries:
  orders: ["**/orders/**"] # the order flow
  billing: ["web/billing/**"]
layers:
  api: ["**/api"]
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	config := NewConfig(root)
	place := func(path string, placement rules.Placement) (rules.Placement, string) {
		t.Helper()
		if _, err := config.Place(path, placement); err != nil {
			t.Fatalf("Place(%s, %+v): %v", path, placement, err)
		}

		loaded, _, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
		return loaded.Architecture.Resolve(path), string(data)
	}

	// The exact path beats orders' wildcard, and the module keeps its layer.
	got, text := place("web/orders/api", rules.Placement{Layer: graph.LayerBoundary, Boundary: "billing"})
	if got.Boundary != "billing" || got.Layer != "api" || !strings.Contains(text, `billing: ["web/billing/**", "web/orders/api"]`) || !strings.Contains(text, "# the order flow") {
		t.Fatalf("into billing: %+v\n%s", got, text)
	}

	got, text = place("web/orders/api", rules.Placement{Layer: graph.LayerBoundary, Boundary: "orders"})
	if got.Boundary != "orders" || strings.Contains(text, `"web/orders/api"`) {
		t.Fatalf("back to orders must only remove the exact path: %+v\n%s", got, text)
	}

	if got, text = place("web/orders/util", rules.Placement{Layer: graph.LayerShared}); got.Layer != graph.LayerShared || !strings.Contains(text, `shared: ["lib/**", "web/orders/util"]`) {
		t.Fatalf("into shared: %+v\n%s", got, text)
	}

	if got, _ = place("web/orders/util", rules.Placement{Layer: graph.LayerComposition}); got.Layer != graph.LayerComposition {
		t.Fatalf("into composition: %+v", got)
	}

	// Shared comes before boundaries, so its wildcard keeps the module.
	if _, err := config.Place("lib/orders", rules.Placement{Layer: graph.LayerBoundary, Boundary: "orders"}); !errors.Is(err, rules.ErrArchitecture) || !strings.Contains(err.Error(), "would be placed in shared") {
		t.Fatalf("a shared wildcard must refuse the move: %v", err)
	}

	if _, err := config.Place("web/x", rules.Placement{Layer: graph.LayerBoundary, Boundary: "returns"}); !errors.Is(err, rules.ErrArchitecture) {
		t.Fatalf("a move never creates a boundary: %v", err)
	}
}

func TestRenameBoundary(t *testing.T) {
	root := t.TempDir()
	original := "version: 1\narchitecture: modular\nboundaries:\n  orders: [\"orders/**\"] # the order flow\n  billing: [\"billing/**\"]\n"
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	config := NewConfig(root)
	if _, err := config.RenameBoundary("orders", "billing"); !errors.Is(err, rules.ErrArchitecture) {
		t.Fatalf("a taken name: %v", err)
	}

	if _, err := config.RenameBoundary("returns", "refunds"); !errors.Is(err, rules.ErrArchitecture) {
		t.Fatalf("no such boundary: %v", err)
	}

	if _, err := config.RenameBoundary("orders", " "); !errors.Is(err, rules.ErrArchitecture) {
		t.Fatalf("a blank name: %v", err)
	}

	if _, err := config.RenameBoundary("orders", "sales"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(root, ConfigFile))
	if text := string(data); !strings.Contains(text, "boundaries:\n  sales: [\"orders/**\"] # the order flow\n  billing:") {
		t.Fatalf("the rename keeps the globs, the order and the comment:\n%s", text)
	}

	loaded, _, err := config.Load()
	if err != nil || loaded.Architecture.Resolve("orders/cart").Boundary != "sales" {
		t.Fatalf("after the rename: %v %+v", err, loaded.Architecture.Resolve("orders/cart"))
	}
}
