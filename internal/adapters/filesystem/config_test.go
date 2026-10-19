package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
		"architecture: snowflake\n",
		"architecture: hexagonal\nstars:\n  .:\n",
		"architecture: snowflake\nstars: [\"internal\"]\n",
		"architecture: snowflake\nstars:\n  internal: [\"internal/core/**\"]\n",
		"architecture: snowflake\nstars:\n  .:\nboundaries:\n  orders: [\"x/**\"]\n",
		"architecture: snowflake\nstars:\n  internal:\n  pkg:\n",
		"architecture: snowflake\nstars:\n  .:\n  \"internal/*\":\n",
		"architecture: snowflake\ncomposition: [\"cmd/**\"]\nstars:\n  .:\n",
		"architecture: snowflake\nstars:\n  .: {orchestrators: [\"cmd/*\"]}\n",
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
		{"a misspelled stars key", "version: 1\narchitecture: snowflake\nstarz:\n  .:\n", ErrUnknownKey, `line 3: unknown key "starz"; did you mean "stars"?`},
		{"a core under a star", "version: 1\narchitecture: snowflake\nstars:\n  .:\n  internal:\n    core: [\"internal/core\"]\n", ErrUnknownKey, `treaty.yaml: line 6: unknown key "core" under stars.internal; a star maps contracts, assemblers and engines`},
		{"a misspelled orchestrators key", "architecture: snowflake\nstars:\n  .:\n    orchestrator: [\".\"]\n", ErrUnknownKey, `line 4: unknown key "orchestrator" under stars."."; did you mean "orchestrators"?`},
		{"a misspelled assemblers key", "architecture: snowflake\nstars:\n  .:\n    assembler: [\".\"]\n", ErrUnknownKey, `line 4: unknown key "assembler" under stars."."; did you mean "assemblers"?`},
		{"assemblers and orchestrators both", "architecture: snowflake\nstars:\n  .:\n    assemblers: [\".\"]\n    orchestrators: [\"cmd/app\"]\n", rules.ErrArchitecture, `treaty.yaml: line 5: invalid architecture: stars."." lists both assemblers and orchestrators, the earlier name for assemblers; merge orchestrators into assemblers`},
		{"a star's list given twice", "architecture: snowflake\nstars:\n  .:\n  internal/blocks:\n    contracts: [\"internal/blocks/contracts\"]\n    orchestrators: []\n    orchestrators: [\"internal/blocks/kit\"]\n", nil, "treaty.yaml: line 7: orchestrators is listed twice under stars.internal/blocks, first at line 6"},
		{"stars under another architecture", "architecture: hexagonal\nstars:\n  .:\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: stars apply only to the snowflake architecture"},
		{"stars of the earlier shape under another architecture", "architecture: hexagonal\nstars:\n  .: [\"internal/contracts\"]\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: stars apply only to the snowflake architecture"},
		{"a core under a star under another architecture", "architecture: clean\nstars:\n  .:\n    core: [\"internal/core\"]\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: stars apply only to the snowflake architecture"},
		{"stars under no architecture", "stars:\n  .:\n    core: [\"internal/core\"]\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: stars apply only to the snowflake architecture"},
		{"stars as a list", "architecture: snowflake\nstars: [\"internal\"]\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: stars must map each star's directory to its contracts, assemblers and engines"},
		{"a star as a list", "version: 1\narchitecture: snowflake\nstars:\n  .:\n  internal: [\"internal/core/**\"]\n", rules.ErrArchitecture, `treaty.yaml: line 5: invalid architecture: stars.internal is a list, the earlier shape of a star; split its entries into contracts, assemblers and engines, such as internal: {contracts: ["internal/contracts"], assemblers: ["internal"]}`},
		{"the root as a list", "version: 1\narchitecture: snowflake\nstars:\n  .: [\"internal/core\", \"cmd/app\"]\n", rules.ErrArchitecture, `treaty.yaml: line 4: invalid architecture: stars."." is a list, the earlier shape of a star; split its entries into contracts, assemblers and engines, such as .: {contracts: ["internal/contracts"], assemblers: ["."]}`},
		{"a star as a word", "architecture: snowflake\nstars:\n  .: internal\n", rules.ErrArchitecture, `line 3: invalid architecture: stars."." must map contracts, assemblers and engines to lists of directories, such as .: {contracts: ["internal/contracts"]}`},
		{"contracts that are not a list", "architecture: snowflake\nstars:\n  .:\n    contracts: {a: b}\n", rules.ErrArchitecture, `treaty.yaml: line 4: invalid architecture: stars.".".contracts must be a list of directories, such as contracts: ["internal/contracts"]`},
		{"engines as a word", "architecture: snowflake\nstars:\n  .:\n  internal/blocks:\n    engines: internal/blocks/run\n", rules.ErrArchitecture, `treaty.yaml: line 5: invalid architecture: stars.internal/blocks.engines must be a list of directories, such as engines: ["internal/blocks/engine"]`},
		{"orchestrators as a word", "architecture: snowflake\nstars:\n  .:\n  internal/blocks:\n    orchestrators: internal/blocks/kit\n", rules.ErrArchitecture, `treaty.yaml: line 5: invalid architecture: stars.internal/blocks.orchestrators must be a list of directories, such as orchestrators: ["internal/blocks"]`},
		{"composition", "architecture: snowflake\ncomposition: [\"cmd/**\"]\nstars:\n  .:\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: composition does not apply to the snowflake architecture: assemblers do its work, so list main packages, factories and the public API among the assemblers of their star"},
		{"no stars", "architecture: snowflake\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: the snowflake architecture needs its stars: declare the repository, ".", with its contracts, assemblers and engines, such as .: {contracts: ["internal/contracts"], assemblers: ["."]}`},
		{"a glob for a star", "architecture: snowflake\nstars:\n  .:\n  \"internal/*\":\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: star "internal/*" must be a directory path relative to the repository root, not a glob`},
		{"a star listed twice", "architecture: snowflake\nstars:\n  .:\n  internal:\n  internal/:\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: star internal is listed twice"},
		{"no root", "architecture: snowflake\nstars:\n  internal/gate:\n  internal/blocks:\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: the snowflake's root star is the repository itself: declare "." under stars, around internal/blocks and internal/gate`},
		{"an unclean entry", "architecture: snowflake\nstars:\n  .: {contracts: [\"./contracts\"]}\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: contracts entry "./contracts" of star . must be a directory path relative to the repository root, with no empty, . or .. segment`},
		{"a glob entry", "architecture: snowflake\nstars:\n  .: {orchestrators: [\"cmd/*\"]}\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: assemblers entry "cmd/*" of star . is a glob; name each package by its directory, so a new directory stays a spoke until treaty.yaml names it`},
		{"an entry outside its star", "architecture: snowflake\nstars:\n  .:\n  internal: {contracts: [\"pkg/contracts\"]}\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: contracts entry "pkg/contracts" of star internal lies outside internal; a star's core is inside its directory`},
		{"an entry inside a branch", "architecture: snowflake\nstars:\n  .: {contracts: [\"internal/blocks/contracts\"]}\n  internal/blocks:\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: contracts entry "internal/blocks/contracts" of star . lies inside branch internal/blocks, whose core is its own; list it under internal/blocks`},
		{"an entry holding a star", "architecture: snowflake\nstars:\n  .: {orchestrators: [\"internal\"]}\n  internal/blocks:\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: assemblers entry "internal" of star . holds star internal/blocks; a core package is one package and those nested in it, never a star`},
		{"an entry listed twice", "architecture: snowflake\nstars:\n  .: {orchestrators: [\"internal/executor\", \"internal/executor\"]}\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: assemblers entry "internal/executor" of star . is listed twice`},
		{"an entry in both lists", "architecture: snowflake\nstars:\n  .: {contracts: [\"internal/executor\"], orchestrators: [\"internal/executor\"]}\n", rules.ErrArchitecture, `treaty.yaml: invalid architecture: "internal/executor" is listed under both the contracts and the assemblers of star .; a core package has one role`},
		{"layers under snowflake", "architecture: snowflake\nstars:\n  .:\nlayers:\n  web: [\"web/**\"]\n", rules.ErrArchitecture, "treaty.yaml: invalid architecture: layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share"},
		{"a misspelled adapter side", "layers:\n  adapter:\n    drivng: [\"web/**\"]\n", ErrUnknownKey, `unknown key "drivng" under layers.adapter; did you mean "driving"?`},
		{"a misspelled rule", "rules:\n  fail_on: [breaking_changes, layer_violations]\n", ErrUnknownRule, `line 2: fail_on: unknown rule "layer_violations"; did you mean "layer_violation"?`},
		{"a finding that is not a rule", "rules:\n  fail_on: [new_contract]\n", ErrUnknownRule, "fail_on takes breaking_changes, layer_violation, dependency_cycles, unclassified, ambiguous_boundaries"},
		{"an unknown warning", "rules:\n  warn_on: [everything]\n", ErrUnknownRule, "warn_on takes breaking_changes"},
		{"an old rule name", "version: 1\nrules:\n  fail_on: [breaking]\n", ErrUnknownRule, `line 3: fail_on: unknown rule "breaking"; since version 1 it is "breaking_changes"`},
		{"a rule name from before version 1", "rules:\n  fail_on: [cycle]\n", ErrUnknownRule, `line 2: fail_on: unknown rule "cycle"; since version 1 it is "dependency_cycles"`},
		{"slices from before boundaries", "architecture: modular\nslices: [\"internal/*\"]\n", ErrUnknownKey, `unknown key "slices"`},
		{"an ignore without a reason", "rules:\n  ignore:\n    - finding: unclassified\n      target: go:web\n      fingerprint: \"0a1b2c3d4e5f\"\n", ErrIgnore, "line 3: invalid ignore entry: the entry has no reason: say why it is ignored"},
		{"an ignore with an empty reason", "rules:\n  ignore:\n    - finding: unclassified\n      target: go:web\n      fingerprint: \"0a1b2c3d4e5f\"\n      reason: \" \"\n", ErrIgnore, "line 6: invalid ignore entry: reason must be text, and not empty"},
		{"a misspelled finding", "rules:\n  ignore:\n    - finding: unclasified\n      target: go:web\n      fingerprint: \"0a1b2c3d4e5f\"\n      reason: scratch\n", ErrIgnore, `"unclasified" is not a kind of finding that can be ignored; did you mean "unclassified"?`},
		{"an implementation finding", "rules:\n  ignore:\n    - finding: implementation\n      target: go:web\n      fingerprint: \"0a1b2c3d4e5f\"\n      reason: scratch\n", ErrIgnore, `"implementation" is not a kind of finding that can be ignored`},
		{"a misspelled ignore key", "rules:\n  ignore:\n    - finding: unclassified\n      target: go:web\n      fingerprnt: \"0a1b2c3d4e5f\"\n      reason: scratch\n", ErrUnknownKey, `unknown key "fingerprnt" under rules.ignore; did you mean "fingerprint"?`},
		{"an ignore given twice", "rules:\n  ignore:\n    - {finding: unclassified, target: go:web, fingerprint: \"1\", reason: a}\n    - {finding: unclassified, target: go:web, fingerprint: \"2\", reason: b}\n", ErrIgnore, "line 4: invalid ignore entry: line 3 already ignores unclassified go:web"},
		{"ignore that is not a list", "rules:\n  ignore: go:web\n", ErrIgnore, "rules.ignore must be a list of entries"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}

			_, found, err := NewConfig(root).Load()
			if !found || err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("found %t, err %v, want %v", found, err, c.want)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q should say %q", err, c.says)
			}
		})
	}
}

func TestLoadSnowflake(t *testing.T) {
	root := t.TempDir()
	text := `# Snowflake: the repository is a star of stars.
version: 1
architecture: snowflake
stars: # each star's directory -> its contracts, assemblers and engines
  .:
    contracts:  ["internal/contracts"]
    assemblers: [".", "internal/assembly"]
    engines:    ["internal/executor"]
  internal/blocks/: {contracts: ["internal/blocks/contracts"], assemblers: ["internal/blocks/kit"]}
  ./internal/gate:
    contracts: ["internal/gate/contracts"]
    orchestrators: ["internal/gate/pipeline"]
  internal/billing:
  internal/reports: {}
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
  ignore:
    - finding: layer_violation
      target: go:internal/plan -> go:internal/tracer
      fingerprint: "3f9c0a1d7b22"
      reason: The planner still calls the tracer directly; it moves behind a Tracer port in internal/contracts next
`
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	config, _, err := NewConfig(root).Load()
	if err != nil {
		t.Fatal(err)
	}

	// The keys under stars are the config's own directories, not keys
	// treaty knows, and keep their order; internal/blocks/ and
	// ./internal/gate name directories, and entries are kept as written.
	want := []rules.Star{
		{Dir: ".", Contracts: []string{"internal/contracts"}, Assemblers: []string{".", "internal/assembly"}, Engines: []string{"internal/executor"}},
		{Dir: "internal/blocks", Contracts: []string{"internal/blocks/contracts"}, Assemblers: []string{"internal/blocks/kit"}},
		// orchestrators, v2's name, is read as assemblers, marked deprecated.
		{Dir: "internal/gate", Contracts: []string{"internal/gate/contracts"}, Assemblers: []string{"internal/gate/pipeline"}, Deprecated: true},
		{Dir: "internal/billing"},
		{Dir: "internal/reports"},
	}

	if config.Architecture.Style != rules.StyleSnowflake || !reflect.DeepEqual(config.Architecture.Stars, want) {
		t.Errorf("stars %+v, want %+v", config.Architecture.Stars, want)
	}

	if len(config.Ignores) != 1 || config.Ignores[0].Finding != rules.FindingLayerViolation || config.Ignores[0].Target != rules.EdgeTarget("go:internal/plan", "go:internal/tracer") {
		t.Errorf("a sideways dependency is ignored like any other violation: %+v", config.Ignores)
	}

	bound, err := config.Architecture.Bind([]string{".", "internal/contracts", "internal/gate/contracts", "internal/gate/pipeline", "internal/gate/tagger"})
	if err != nil {
		t.Fatal(err)
	}

	if got := bound.Resolve("internal/gate/tagger"); !got.Same(rules.Placement{Layer: graph.LayerLeaf, Star: "internal/gate", Spoke: "internal/gate/tagger"}) {
		t.Errorf("internal/gate/tagger resolved to %+v", got)
	}

	if got := bound.Resolve("internal/gate/pipeline"); !got.Same(rules.Placement{Layer: graph.LayerAssembler, Star: "internal/gate", Entry: "internal/gate/pipeline"}) {
		t.Errorf("internal/gate/pipeline resolved to %+v", got)
	}

	if notes := bound.Notes(); !slices.ContainsFunc(notes, func(note string) bool { return strings.HasPrefix(note, "star internal/gate lists orchestrators") }) {
		t.Errorf("no note on the deprecated key: %q", notes)
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

func TestEditsThatChangeNothingDoNotWrite(t *testing.T) {
	// Written the way no encoder would write it, so any rewrite shows.
	const original = "version: 1\narchitecture:   layered\nlayers:\n  web:   [ 'web/**' ]   # top\n  store: [ \"store/**\" ]\n"
	root := t.TempDir()
	file := filepath.Join(root, ConfigFile)
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	config := NewConfig(root)
	if _, err := config.Place("web/home", rules.Placement{Layer: "web"}); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Save(original, original); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Unignore("unclassified", "go:tools"); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(file); string(data) != original {
		t.Fatalf("an edit that changes nothing rewrote treaty.yaml:\n%s", data)
	}
}

func TestPlaceInSnowflake(t *testing.T) {
	const original = `# Our snowflake.
version: 1
architecture: snowflake
stars:
  .:
    contracts: # what every spoke shares
      - internal/contracts
    orchestrators: ["."]
  internal/blocks/: {contracts: ["internal/blocks/contracts"]}
  internal/gate:
rules:
  fail_on: [breaking_changes, layer_violation]
`
	root := t.TempDir()
	file := filepath.Join(root, ConfigFile)
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
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

		data, _ := os.ReadFile(file)
		return string(data)
	}

	// A leaf moves into the contracts of its star by its directory,
	// appended to a block list with its comment kept; then among the
	// assemblers, kept under the orchestrators key the star still uses, and
	// out again to its own spoke.
	text := place("internal/web", rules.Placement{Layer: graph.LayerContracts, Star: ".", Entry: "internal/web"})
	if !strings.Contains(text, "# Our snowflake.") || !strings.Contains(text, "contracts: # what every spoke shares\n      - internal/contracts\n      - internal/web\n") {
		t.Fatalf("internal/web joins the contracts of .:\n%s", text)
	}

	text = place("internal/web", rules.Placement{Layer: graph.LayerAssembler, Star: ".", Entry: "internal/web"})
	if !strings.Contains(text, `orchestrators: [".", "internal/web"]`) || strings.Contains(text, "- internal/web") || strings.Contains(text, "assemblers") {
		t.Fatalf("internal/web moves from the contracts to the assemblers of ., under orchestrators:\n%s", text)
	}

	text = place("internal/web", rules.Placement{Layer: graph.LayerEngine, Star: ".", Entry: "internal/web"})
	if !strings.Contains(text, `orchestrators: ["."]`+"\n    engines: [\"internal/web\"]\n") {
		t.Fatalf("internal/web moves among the engines of .:\n%s", text)
	}

	// A package beneath a core package moves into the core by its own
	// directory, since an entry names exactly one package.
	if text = place("internal/contracts/grades", rules.Placement{Layer: graph.LayerContracts, Star: ".", Entry: "internal/contracts/grades"}); !strings.Contains(text, "      - internal/contracts/grades\n") {
		t.Fatalf("a nested package joins the contracts:\n%s", text)
	}

	if text = place("internal/web", rules.Placement{Layer: graph.LayerLeaf, Star: ".", Spoke: "internal/web"}); strings.Contains(text, "internal/web") {
		t.Fatalf("internal/web leaves the core:\n%s", text)
	}

	// A star whose key has a trailing slash, or no value yet, takes a
	// module into its core all the same, its list created as a flow list,
	// contracts before assemblers, assemblers before engines.
	if text = place("internal/blocks/run", rules.Placement{Layer: graph.LayerEngine, Star: "internal/blocks", Entry: "internal/blocks/run"}); !strings.Contains(text, `internal/blocks/: {contracts: ["internal/blocks/contracts"], engines: ["internal/blocks/run"]}`) {
		t.Fatalf("a star written inline gets its engines:\n%s", text)
	}

	if text = place("internal/blocks/kit", rules.Placement{Layer: graph.LayerAssembler, Star: "internal/blocks", Entry: "internal/blocks/kit"}); !strings.Contains(text, `internal/blocks/: {contracts: ["internal/blocks/contracts"], assemblers: ["internal/blocks/kit"], engines: ["internal/blocks/run"]}`) {
		t.Fatalf("a star written inline gets its assemblers before its engines:\n%s", text)
	}

	if text = place("internal/gate/contracts", rules.Placement{Layer: graph.LayerContracts, Star: "internal/gate", Entry: "internal/gate/contracts"}); !strings.Contains(text, "internal/gate:\n    contracts: [\"internal/gate/contracts\"]\n") {
		t.Fatalf("a star with no value gets a mapping with its list:\n%s", text)
	}

	if text = place("internal/gate", rules.Placement{Layer: graph.LayerAssembler, Star: "internal/gate", Entry: "internal/gate"}); !strings.Contains(text, "    contracts: [\"internal/gate/contracts\"]\n    assemblers: [\"internal/gate\"]\n") {
		t.Fatalf("a star's own package joins its assemblers:\n%s", text)
	}

	// An edit that changes nothing does not write.
	before, _ := os.ReadFile(file)
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Place("internal/gate", rules.Placement{Layer: graph.LayerAssembler, Star: "internal/gate", Entry: "internal/gate"}); err != nil {
		t.Fatalf("a move that changes nothing: %v", err)
	}

	if _, err := config.Place("internal/plan", rules.Placement{Layer: graph.LayerLeaf, Star: ".", Spoke: "internal/plan"}); err != nil {
		t.Fatalf("a leaf moved to its own spoke: %v", err)
	}

	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}

	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatalf("an edit that changes nothing rewrote treaty.yaml:\n%s", after)
	}

	for _, c := range []struct {
		name, path string
		placement  rules.Placement
		says       string
	}{
		{"a package beneath a core package out to a spoke", "internal/contracts/sub", rules.Placement{Layer: graph.LayerLeaf, Star: ".", Spoke: "internal/contracts/sub"}, "it lies beneath core package internal/contracts, so it is never a spoke: list it in the core, or move its files out of internal/contracts"},
		{"another star's core", "internal/plan", rules.Placement{Layer: graph.LayerContracts, Star: "internal/blocks"}, "a core package is a child of its own star: drop it on the core of star ."},
		{"a missing star", "internal/plan", rules.Placement{Layer: graph.LayerContracts, Star: "internal/orders"}, `no star "internal/orders"`},
		{"another spoke", "internal/plan", rules.Placement{Layer: graph.LayerLeaf, Star: ".", Spoke: "internal/web"}, "a spoke is its directory: move the package's files into internal/web to put it there"},
		{"an own package out of its core", ".", rules.Placement{Layer: graph.LayerLeaf, Star: ".", Spoke: "."}, "a star's own package is always in its core: drop it on its contracts, assemblers or engines"},
		{"composition", "cmd/app", rules.Placement{Layer: graph.LayerComposition}, "composition does not apply to the snowflake architecture"},
	} {
		if _, err := config.Place(c.path, c.placement); !errors.Is(err, rules.ErrArchitecture) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %q", c.name, err, c.says)
		}
	}
}
