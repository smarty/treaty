// Package filesystem reads treaty.yaml and owns the .treaty workspace.
package filesystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

const (
	ConfigFile = "treaty.yaml"
	Directory  = ".treaty"

	serverFile = "server.json"
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

// serverInfo is the shape of .treaty/server.json.
type serverInfo struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

// configFile is the YAML shape of treaty.yaml.
type configFile struct {
	Layers struct {
		Composition []string `yaml:"composition"`
		Domain      []string `yaml:"domain"`
		Application []string `yaml:"application"`
		Adapter     struct {
			Driving []string `yaml:"driving"`
			Driven  []string `yaml:"driven"`
		} `yaml:"adapter"`
	} `yaml:"layers"`
	Rules struct {
		FailOn            []string `yaml:"fail_on"`
		WarnOn            []string `yaml:"warn_on"`
		StrengthThreshold *float64 `yaml:"strength_threshold"`
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

// Load reads treaty.yaml, falling back to defaults when it is absent.
//
// Returns:
//   - config: the parsed config.
//   - found: false when the file does not exist.
//   - err: the file could not be read or parsed.
func (this *Config) Load() (config app.Config, found bool, err error) {
	config = app.Config{
		FailOn:            []string{rules.FindingBreaking, rules.FindingLayerViolation},
		WarnOn:            []string{rules.FindingUnclassified, "weak_contract"},
		StrengthThreshold: 0.60,
	}

	data, err := os.ReadFile(filepath.Join(this.root, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return config, false, nil
	}

	if err != nil {
		return config, false, err
	}

	var file configFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return config, true, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	config.Layers = rules.Layers{
		Composition: file.Layers.Composition,
		Domain:      file.Layers.Domain,
		Application: file.Layers.Application,
		Driving:     file.Layers.Adapter.Driving,
		Driven:      file.Layers.Adapter.Driven,
	}

	if file.Rules.FailOn != nil {
		config.FailOn = file.Rules.FailOn
	}

	if file.Rules.WarnOn != nil {
		config.WarnOn = file.Rules.WarnOn
	}

	if file.Rules.StrengthThreshold != nil {
		config.StrengthThreshold = *file.Rules.StrengthThreshold
	}

	return config, true, nil
}

// Fingerprint hashes the path, size and modification time of every file
// that can change the graph: everything outside hidden, vendor and
// dependency directories, plus treaty.yaml and the designs.
//
// Returns:
//   - result: the fingerprint.
//   - err: the tree could not be walked.
func (this *Watcher) Fingerprint() (result string, err error) {
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
		return nil
	})

	return fmt.Sprintf("%016x", hasher.Sum64()), err
}

// Announce writes .treaty/server.json with the live map's URL.
//
// Parameters:
//   - url: where the live map is served.
//
// Returns:
//   - withdraw: removes the file if it still names this URL.
//   - err: the file could not be written.
func (this *Workspace) Announce(url string) (withdraw func(), err error) {
	if err := this.Init(); err != nil {
		return nil, err
	}

	path := filepath.Join(this.root, Directory, serverFile)
	data, _ := json.Marshal(serverInfo{URL: url, PID: os.Getpid()})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}

	return func() {
		if announced, _ := this.Announced(); announced == url {
			_ = os.Remove(path)
		}
	}, nil
}

// Announced reads the URL in .treaty/server.json.
//
// Returns:
//   - url: the announced URL, or empty when none is recorded.
//   - err: the file exists but could not be read.
func (this *Workspace) Announced() (url string, err error) {
	data, err := os.ReadFile(filepath.Join(this.root, Directory, serverFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	var info serverInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return "", nil
	}

	return info.URL, nil
}

// Designs lists the names of the designs in .treaty/designs.
//
// Returns:
//   - result: design names, sorted, without the .cml extension.
//   - err: the directory could not be read.
func (this *Workspace) Designs() (result []string, err error) {
	entries, err := os.ReadDir(filepath.Join(this.root, Directory, "designs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".cml"); ok && !entry.IsDir() && designName.MatchString(name) {
			result = append(result, name)
		}
	}

	sort.Strings(result)
	return result, err
}

// SaveDesign writes .treaty/designs/<name>.cml, replacing an existing one.
//
// Parameters:
//   - name: the design name.
//   - text: the CML text.
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

	return path, os.WriteFile(path, []byte(text), 0o644)
}

// CreateDesign writes .treaty/designs/<name>.cml, refusing to overwrite.
//
// Parameters:
//   - name: the design name.
//   - text: the CML text.
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

// ReadDesign reads .treaty/designs/<name>.cml.
//
// Parameters:
//   - name: the design name.
//
// Returns:
//   - result: the CML text.
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
		return "", fmt.Errorf("%w: %s", ErrNoDesign, name)
	}

	return string(data), err
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

func (this *Workspace) designPath(name string) (string, error) {
	if !designName.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrDesignName, name)
	}

	return filepath.Join(this.root, Directory, "designs", name+".cml"), nil
}
