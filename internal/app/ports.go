// Package app holds the use cases of Treaty and the ports every side
// effect sits behind. It never calls a parser, git or the filesystem directly.
package app

import (
	"context"
	"encoding/json"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const KindField = "field"

const (
	RegistrationAdded     = "added"
	RegistrationCreated   = "created"
	RegistrationReplaced  = "replaced"
	RegistrationUnchanged = "unchanged"
)

// AgentConfig is where coding agents find a repository's MCP servers, such
// as the .mcp.json that Claude Code reads.
type AgentConfig interface {
	// Register adds one MCP server and keeps every other entry.
	//
	// Parameters:
	//   - name: the server's name.
	//   - server: how to start it.
	//   - replace: overwrite a different entry with the same name.
	//
	// Returns:
	//   - result: where it was written and what happened.
	//   - err: the file is not valid, or holds a different entry with that
	//     name and replace is false.
	Register(name string, server MCPServer, replace bool) (result Registration, err error)
}

// MCPServer is how an agent starts an MCP server over stdio.
type MCPServer struct {
	Command string
	Args    []string
}

// Registration reports what AgentConfig.Register did.
type Registration struct {
	Path    string
	Outcome string
}

// Config is the parsed treaty.yaml.
type Config struct {
	Architecture rules.Architecture
	FailOn       []string
	WarnOn       []string
}

// ConfigSource loads the layer config.
type ConfigSource interface {
	// Load reads the config.
	//
	// Returns:
	//   - config: the parsed config.
	//   - found: false when no config file exists.
	//   - err: a read or parse failure.
	Load() (config Config, found bool, err error)

	// Place edits the config so one module resolves to a placement, keeping
	// the rest of the file as it is.
	//
	// Returns:
	//   - path: where the config was written.
	//   - err: there is no config, or the edit would not place the module
	//     there.
	Place(modulePath string, placement rules.Placement) (path string, err error)
}

// Declaration is what a dialect learns from one AutoPen declaration line.
type Declaration struct {
	Kind     string
	Name     string
	Contract bool
	Key      string
}

// Dialect is one language's knowledge of AutoPen declaration lines.
type Dialect interface {
	// Compatible applies the language's breaking-change rules.
	Compatible(before, after *graph.Symbol) bool

	// Declare classifies one declaration line.
	//
	// Parameters:
	//   - text: the declaration text.
	//   - parent: the enclosing declaration, or nil at the top level.
	//
	// Returns:
	//   - result: the kind, name, contract flag and comparison key.
	//   - err: the line is not a declaration in this language.
	Declare(text string, parent *Declaration) (result Declaration, err error)

	// IsFile reports whether a header path names a source file.
	IsFile(path string) bool

	// Language is the id prefix, such as go.
	Language() string

	// ModulePath derives the module path from a header path.
	ModulePath(path string) string
}

// MapRenderer draws the interactive map.
type MapRenderer interface {
	// Page produces the live page, which loads its data from the server.
	Page() []byte

	// Payload lays out a view and encodes it for the live page.
	Payload(view MapView) ([]byte, error)

	// Render produces a self-contained HTML page.
	Render(view MapView) ([]byte, error)
}

// SourceExtractor builds a graph from a source tree.
type SourceExtractor interface {
	// Extract reads every supported source file under root.
	Extract(root string) (*graph.Graph, error)

	// Source reads one file under root, for embedding in the map.
	Source(root, file string) ([]byte, error)
}

// Preferences are one person's choices on the live map, kept across sessions
// and repositories: the theme, whether the map follows Claude, and the panel
// layout. Layout is the page's own JSON, kept as it is. A nil or empty field
// means no choice has been made.
type Preferences struct {
	Theme  string          `json:"theme,omitempty"`
	Follow *bool           `json:"follow,omitempty"`
	Legend *bool           `json:"legend,omitempty"`
	Layout json.RawMessage `json:"layout,omitempty"`
}

// Position is where a person put a module on the map, in map units.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Positions are the places a person put modules on one repository's map:
// for each architecture the map can draw, each module's position.
type Positions map[string]map[string]Position

// PreferenceStore keeps a person's preferences.
type PreferenceStore interface {
	// Load reads the preferences, empty when none have been saved.
	Load() (Preferences, error)

	// Save replaces the preferences.
	Save(preferences Preferences) error
}

// TestSuite finds and runs one language's tests.
type TestSuite interface {
	// Discover finds every test under root and the symbols each one uses.
	//
	// Parameters:
	//   - root: the repository root.
	//   - g: the graph of root, which test references resolve against.
	//
	// Returns:
	//   - result: the tests, without ids.
	//   - err: a test file could not be read.
	Discover(root string, g *graph.Graph) (result []TestCase, err error)

	// Language is the id prefix of the modules whose tests this suite runs,
	// such as go.
	Language() string

	// Run runs tests and reports each outcome as it arrives.
	//
	// Notes:
	//   - An outcome with no name is about a whole module, such as one that
	//     does not build.
	//
	// Parameters:
	//   - ctx: cancels the run.
	//   - root: the repository root.
	//   - requests: the tests to run, by module.
	//   - report: receives every outcome; it is never called concurrently.
	//
	// Returns:
	//   - coverage: the lines the run covered and missed, by file path
	//     relative to root.
	//   - err: the tests could not be started.
	Run(ctx context.Context, root string, requests []TestRequest, report func(TestOutcome)) (coverage map[string]LineCoverage, err error)
}

// Theme is a named set of the map's color tokens, such as bg, ink, added
// and violation. Base is light or dark. Group is standard, accessibility or
// style for the themes Treaty ships, and yours for a person's own. Default
// marks a theme Treaty ships and rewrites.
type Theme struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Base    string            `json:"base"`
	Group   string            `json:"group"`
	Default bool              `json:"default"`
	Colors  map[string]string `json:"colors"`
}

// ThemeSource provides the map's themes: the defaults and any a person adds.
type ThemeSource interface {
	// Install writes the default themes where people keep theirs, replacing
	// earlier copies of them and removing defaults that no longer ship.
	Install() error

	// Themes lists every theme, the defaults first. A theme that cannot be
	// read is left out and reported in err, alongside the others.
	Themes() (result []Theme, err error)
}

// VersionControl materializes other revisions of the repository.
type VersionControl interface {
	// DefaultBranch names the branch pull requests merge into by default,
	// such as origin/main.
	DefaultBranch() (branch string, err error)

	// MergeBase finds the commit where two revisions diverged.
	MergeBase(a, b string) (commit string, err error)

	// Resolve turns a revision into a commit id.
	Resolve(ref string) (commit string, err error)

	// Materialize writes the tree at ref into a temporary directory.
	//
	// Returns:
	//   - dir: the directory holding the tree.
	//   - cleanup: removes the directory.
	//   - err: the ref could not be read.
	Materialize(ref string) (dir string, cleanup func(), err error)
}

// Watcher notices changes to the working tree.
type Watcher interface {
	// Fingerprint summarizes every file that can change the graph, the
	// config or a design; it differs whenever one of them changes. Files
	// counts the files it covered, which sizes the repository.
	Fingerprint() (fingerprint string, files int, err error)
}

// Workspace is the .treaty directory: designs and rendered output.
type Workspace interface {
	// CreateDesign writes a new design, failing if it already exists.
	CreateDesign(name, text string) (path string, err error)

	// Init creates the workspace and its self-ignoring .gitignore.
	Init() error

	// Designs lists the design names.
	Designs() ([]string, error)

	// Positions reads where the person put modules on the map, empty when
	// nothing has been moved.
	Positions() (Positions, error)

	// ReadDesign reads a design by name.
	ReadDesign(name string) (string, error)

	// SaveDesign writes a design, replacing one with the same name.
	SaveDesign(name, text string) (path string, err error)

	// SavePositions replaces the saved module positions.
	SavePositions(positions Positions) error

	// WriteConfig replaces the config.
	WriteConfig(text string) (path string, err error)

	// WriteConfigDraft writes a proposed config without overwriting one.
	WriteConfigDraft(text string) (path string, err error)

	// WriteOutput writes a rendered artifact, returning its path.
	WriteOutput(name string, data []byte) (path string, err error)
}
