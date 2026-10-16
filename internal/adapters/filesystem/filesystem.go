// Package filesystem reads treaty.yaml and owns the .treaty workspace.
package filesystem

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const (
	ConfigFile = "treaty.yaml"
	Directory  = ".treaty"

	// DesignExtension ends an AutoPen design's file name. Designs saved
	// before the language was renamed end in legacyExtension and still load.
	DesignExtension = ".pen"
	legacyExtension = ".cml"
)

var (
	ErrDesignExists = errors.New("design already exists")
	ErrDesignName   = errors.New("design names use letters, digits, dots, dashes and underscores")
	ErrNoDesign     = errors.New("no such design")

	designName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Config loads treaty.yaml from the repository root.
type Config struct {
	root string
}

// Watcher fingerprints the working tree to notice changes.
type Watcher struct {
	root string
}

// Workspace is the .treaty directory at the repository root.
type Workspace struct {
	root string
}

// configFile is the YAML shape of treaty.yaml. Layers stays a node because
// its order carries meaning: top to bottom for layered and vertical.
// Boundaries and Stars stay nodes to keep the order they are listed in.
type configFile struct {
	Version      int       `yaml:"version"`
	Architecture string    `yaml:"architecture"`
	Composition  []string  `yaml:"composition"`
	Layers       yaml.Node `yaml:"layers"`
	Shared       []string  `yaml:"shared"`
	Boundaries   yaml.Node `yaml:"boundaries"`
	Public       []string  `yaml:"public"`
	Stars        yaml.Node `yaml:"stars"`
	Rules        struct {
		FailOn []string  `yaml:"fail_on"`
		WarnOn []string  `yaml:"warn_on"`
		Ignore yaml.Node `yaml:"ignore"`
	} `yaml:"rules"`
}

// NewConfig creates a config source for a repository.
//
// Parameters:
//   - root: the repository root.
//
// Returns:
//   - result: the config source.
func NewConfig(root string) *Config {
	return &Config{root: root}
}

// NewWatcher creates a watcher for a repository.
//
// Parameters:
//   - root: the repository root.
//
// Returns:
//   - result: the watcher.
func NewWatcher(root string) *Watcher {
	return &Watcher{root: root}
}

// NewWorkspace creates the workspace for a repository.
//
// Parameters:
//   - root: the repository root.
//
// Returns:
//   - result: the workspace.
func NewWorkspace(root string) *Workspace {
	return &Workspace{root: root}
}

// Load reads treaty.yaml, falling back to defaults when it is absent. The
// file is read strictly: see decodeConfig.
//
// Returns:
//   - config: the parsed config.
//   - found: false when the file does not exist.
//   - err: the file could not be read or parsed.
func (this *Config) Load() (config app.Config, found bool, err error) {
	config = app.Config{
		Architecture: rules.Architecture{Style: rules.StyleNone},
		FailOn:       []string{rules.FindingBreaking, rules.FindingLayerViolation, rules.FindingCycle},
		WarnOn:       []string{rules.FindingUnclassified, rules.FindingAmbiguous},
	}

	data, err := os.ReadFile(filepath.Join(this.root, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return config, false, nil
	}

	if err != nil {
		return config, false, err
	}

	file, versioned, err := decodeConfig(data)
	if err != nil {
		return config, true, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	config.Unversioned = !versioned
	if config.Architecture, err = file.architecture(); err != nil {
		return config, true, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	if file.Rules.FailOn != nil {
		config.FailOn = ruleKinds(file.Rules.FailOn)
	}

	if file.Rules.WarnOn != nil {
		config.WarnOn = ruleKinds(file.Rules.WarnOn)
	}

	config.Ignores = ignores(&file.Rules.Ignore)
	return config, true, nil
}

// Text reads treaty.yaml as it is.
//
// Returns:
//   - text: the file's text; empty when there is no file.
//   - found: false when the file does not exist.
//   - err: the file could not be read.
func (this *Config) Text() (text string, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(this.root, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}

	return string(data), err == nil, err
}

// Save replaces treaty.yaml with text, after reading it as Load does, when
// the file still holds expected. Empty text with no file to replace writes
// an empty file, which declares no architecture.
//
// Parameters:
//   - text: the new file.
//   - expected: what the file holds now, empty when there is no file.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: the text is invalid, the file changed, or the write failed.
//
// Errors:
//   - app.ErrConfigInvalid: Load would refuse the text.
//   - app.ErrConfigChanged: the file no longer holds expected.
func (this *Config) Save(text, expected string) (path string, err error) {
	file, _, err := decodeConfig([]byte(text))
	if err == nil {
		_, err = file.architecture()
	}

	if err != nil {
		return "", fmt.Errorf("%w: %w", app.ErrConfigInvalid, err)
	}

	current, _, err := this.Text()
	if err != nil {
		return "", err
	}

	if current != expected {
		return "", app.ErrConfigChanged
	}

	path = filepath.Join(this.root, ConfigFile)
	return path, writeChanged(path, []byte(text))
}

// Fingerprint hashes the path, size and modification time of every file
// that can change the graph: everything outside hidden, vendor and
// dependency directories, plus treaty.yaml and the designs.
//
// Returns:
//   - result: the fingerprint.
//   - files: how many files it covers.
//   - err: the tree could not be walked.
func (this *Watcher) Fingerprint() (result string, files int, err error) {
	hasher := fnv.New64a()
	workspace := filepath.Join(this.root, Directory)
	designs := filepath.Join(workspace, "designs")
	err = filepath.WalkDir(this.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := entry.Name()
		if entry.IsDir() {
			hidden := strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules"
			if hidden && current != this.root && current != workspace {
				return filepath.SkipDir
			}

			return nil
		}

		if filepath.Dir(current) != designs && strings.HasPrefix(current, workspace+string(filepath.Separator)) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		fmt.Fprintf(hasher, "%s\x00%d\x00%d\n", current, info.Size(), info.ModTime().UnixNano())
		files++
		return nil
	})

	return fmt.Sprintf("%016x", hasher.Sum64()), files, err
}

// Designs lists the names of the designs in .treaty/designs.
//
// Returns:
//   - result: design names, sorted, without their .pen or older .cml
//     extension.
//   - err: the directory could not be read.
func (this *Workspace) Designs() (result []string, err error) {
	entries, err := os.ReadDir(filepath.Join(this.root, Directory, "designs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), DesignExtension)
		if !ok {
			name, ok = strings.CutSuffix(entry.Name(), legacyExtension)
		}

		if ok && !entry.IsDir() && designName.MatchString(name) && !slices.Contains(result, name) {
			result = append(result, name)
		}
	}

	sort.Strings(result)
	return result, err
}

// SaveDesign writes .treaty/designs/<name>.pen, replacing an existing one,
// including one saved as .cml.
//
// Parameters:
//   - name: the design name.
//   - text: the AutoPen text.
//
// Returns:
//   - path: where the design was written.
//   - err: the name is invalid or the write failed.
//
// Errors:
//   - ErrDesignName: the name has characters outside the allowed set.
func (this *Workspace) SaveDesign(name, text string) (path string, err error) {
	path, err = this.designPath(name)
	if err != nil {
		return "", err
	}

	if err := this.Init(); err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}

	if err := os.Remove(legacy(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	return path, nil
}

// CreateDesign writes .treaty/designs/<name>.pen, refusing to overwrite a
// design of that name, whether .pen or .cml.
//
// Parameters:
//   - name: the design name.
//   - text: the AutoPen text.
//
// Returns:
//   - path: where the design was written.
//   - err: the name is invalid, the design exists or the write failed.
//
// Errors:
//   - ErrDesignName: the name has characters outside the allowed set.
//   - ErrDesignExists: a design with that name already exists.
func (this *Workspace) CreateDesign(name, text string) (path string, err error) {
	path, err = this.designPath(name)
	if err != nil {
		return "", err
	}

	if err := this.Init(); err != nil {
		return "", err
	}

	if _, err := os.Stat(legacy(path)); err == nil {
		return "", fmt.Errorf("%w: %s", ErrDesignExists, legacy(path))
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%w: %s", ErrDesignExists, path)
	}

	if err != nil {
		return "", err
	}

	if _, err := file.WriteString(text); err != nil {
		_ = file.Close()
		return "", err
	}

	return path, file.Close()
}

// Init creates .treaty with a .gitignore that ignores the directory itself.
//
// Returns:
//   - err: the directory could not be created.
func (this *Workspace) Init() error {
	for _, dir := range []string{"designs", "cache", "out"} {
		if err := os.MkdirAll(filepath.Join(this.root, Directory, dir), 0o755); err != nil {
			return err
		}
	}

	return os.WriteFile(filepath.Join(this.root, Directory, ".gitignore"), []byte("*\n"), 0o644)
}

// ReadDesign reads .treaty/designs/<name>.pen, or <name>.cml when the
// design was saved before the language was renamed.
//
// Parameters:
//   - name: the design name.
//
// Returns:
//   - result: the AutoPen text.
//   - err: the name is invalid or the design does not exist.
//
// Errors:
//   - ErrNoDesign: no design with that name exists.
func (this *Workspace) ReadDesign(name string) (result string, err error) {
	path, err := this.designPath(name)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(legacy(path))
	}

	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrNoDesign, name)
	}

	return string(data), err
}

// WriteConfig replaces treaty.yaml.
//
// Parameters:
//   - text: the new config.
//
// Returns:
//   - path: where the config was written.
//   - err: the write failed.
func (this *Workspace) WriteConfig(text string) (path string, err error) {
	path = filepath.Join(this.root, ConfigFile)
	return path, writeChanged(path, []byte(text))
}

// WriteConfigDraft writes treaty.yaml, or a draft beside the existing
// one in .treaty/out when a config is already present.
//
// Parameters:
//   - text: the proposed config.
//
// Returns:
//   - path: where the draft was written.
//   - err: the write failed.
func (this *Workspace) WriteConfigDraft(text string) (path string, err error) {
	path = filepath.Join(this.root, ConfigFile)
	if _, err := os.Stat(path); err == nil {
		return this.WriteOutput("treaty.draft.yaml", []byte(text))
	}

	return path, os.WriteFile(path, []byte(text), 0o644)
}

// WriteOutput writes .treaty/out/<name>.
//
// Parameters:
//   - name: the file name.
//   - data: the contents.
//
// Returns:
//   - path: where the file was written.
//   - err: the write failed.
func (this *Workspace) WriteOutput(name string, data []byte) (path string, err error) {
	if err := this.Init(); err != nil {
		return "", err
	}

	path = filepath.Join(this.root, Directory, "out", name)
	return path, os.WriteFile(path, data, 0o644)
}

// architecture builds the rules from the file, innermost layer first.
//
// Notes:
//   - With no architecture key there is no architecture, so layers or
//     composition listed without one are an error of Validate.
//
// Returns:
//   - result: the validated architecture.
//   - err: the layers do not parse, or the architecture is invalid.
func (this configFile) architecture() (result rules.Architecture, err error) {
	result = rules.Architecture{Style: this.Architecture, Composition: this.Composition, Shared: this.Shared, Public: this.Public}
	if result.Style == "" {
		result.Style = rules.StyleNone
	}

	if this.Boundaries.Kind != 0 && this.Boundaries.Kind != yaml.MappingNode {
		return result, fmt.Errorf("%w: boundaries must map each boundary's name to its globs", rules.ErrArchitecture)
	}

	for i := 0; i+1 < len(this.Boundaries.Content); i += 2 {
		name, value := this.Boundaries.Content[i].Value, this.Boundaries.Content[i+1]
		var globs []string
		if err := value.Decode(&globs); err != nil {
			return result, fmt.Errorf("boundaries.%s: %w", name, err)
		}

		result.Boundaries = append(result.Boundaries, rules.Boundary{Name: name, Globs: globs})
	}

	if this.Stars.Kind != 0 && this.Stars.Kind != yaml.MappingNode {
		return result, fmt.Errorf("%w: stars must map each star's directory to the globs of its core", rules.ErrArchitecture)
	}

	for i := 0; i+1 < len(this.Stars.Content); i += 2 {
		dir, value := this.Stars.Content[i].Value, this.Stars.Content[i+1]
		var globs []string
		if err := value.Decode(&globs); err != nil {
			return result, fmt.Errorf("stars.%s: %w", dir, err)
		}

		// A glob keeps its spelling for Validate to refuse; only a
		// directory is cleaned, so internal/ and internal name one star.
		if !strings.ContainsAny(dir, "*?[") && dir != "" {
			dir = path.Clean(dir)
		}

		result.Stars = append(result.Stars, rules.Star{Dir: dir, Core: globs})
	}

	if this.Layers.Kind != 0 && this.Layers.Kind != yaml.MappingNode {
		return result, fmt.Errorf("%w: layers must map each layer name to its globs", rules.ErrArchitecture)
	}

	var listed []rules.Layer
	for i := 0; i+1 < len(this.Layers.Content); i += 2 {
		name, value := this.Layers.Content[i].Value, this.Layers.Content[i+1]
		if result.Style == rules.StyleHexagonal && name == graph.LayerAdapter {
			var sides struct {
				Driving []string `yaml:"driving"`
				Driven  []string `yaml:"driven"`
			}

			if err := value.Decode(&sides); err != nil {
				return result, fmt.Errorf("layers.adapter: %w", err)
			}

			listed = append(listed, rules.Layer{Name: name, Side: graph.SideDriving, Globs: sides.Driving}, rules.Layer{Name: name, Side: graph.SideDriven, Globs: sides.Driven})
			continue
		}

		var globs []string
		if err := value.Decode(&globs); err != nil {
			return result, fmt.Errorf("layers.%s: %w", name, err)
		}

		if result.Style == rules.StyleHexagonal && name == graph.LayerComposition {
			result.Composition = append(result.Composition, globs...)
			continue
		}

		listed = append(listed, rules.Layer{Name: name, Globs: globs})
	}

	switch result.Style {
	case rules.StyleHexagonal, rules.StyleClean:
		order := rules.HexagonalLayers
		if result.Style == rules.StyleClean {
			order = rules.CleanLayers
		}

		rank := func(name string) int {
			if index := slices.Index(order, name); index >= 0 {
				return index
			}

			return len(order)
		}

		sort.SliceStable(listed, func(i, j int) bool { return rank(listed[i].Name) < rank(listed[j].Name) })
	default:
		slices.Reverse(listed)
	}

	result.Layers = listed
	return result, result.Validate()
}

func (this *Workspace) designPath(name string) (string, error) {
	if !designName.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrDesignName, name)
	}

	return filepath.Join(this.root, Directory, "designs", name+DesignExtension), nil
}

// legacy is where a design was saved before the language was renamed: the
// same name with the .cml extension.
func legacy(path string) string {
	return strings.TrimSuffix(path, DesignExtension) + legacyExtension
}
