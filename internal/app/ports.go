// Package app holds the use cases of Treaty and the ports every side
// effect sits behind. It never calls a parser, git or the filesystem directly.
package app

import (
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
}

// Declaration is what a dialect learns from one CML declaration line.
type Declaration struct {
	Kind     string
	Name     string
	Contract bool
	Key      string
}

// Dialect is one language's knowledge of CML declaration lines.
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

// Peer is another treaty server already serving this repository's map. A
// session's MCP server follows its baseline and selection instead of
// serving a second map.
type Peer interface {
	// Show forwards a request for the person to look at something.
	Show(target, reason string) error

	// State reads the peer's baseline and selection.
	State() (LiveState, error)
}

// SourceExtractor builds a graph from a source tree.
type SourceExtractor interface {
	// Extract reads every supported source file under root.
	Extract(root string) (*graph.Graph, error)

	// Source reads one file under root, for embedding in the map.
	Source(root, file string) ([]byte, error)
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
	// config or a design; it differs whenever one of them changes.
	Fingerprint() (string, error)
}

// Workspace is the .treaty directory: designs and rendered output.
type Workspace interface {
	// Announce records the URL of the live map served for this repository,
	// so that later sessions can find it.
	//
	// Returns:
	//   - withdraw: removes the record.
	//   - err: the record could not be written.
	Announce(url string) (withdraw func(), err error)

	// Announced reads the recorded URL, or empty when there is none.
	Announced() (url string, err error)

	// CreateDesign writes a new design, failing if it already exists.
	CreateDesign(name, text string) (path string, err error)

	// Init creates the workspace and its self-ignoring .gitignore.
	Init() error

	// Designs lists the design names.
	Designs() ([]string, error)

	// ReadDesign reads a design by name.
	ReadDesign(name string) (string, error)

	// SaveDesign writes a design, replacing one with the same name.
	SaveDesign(name, text string) (path string, err error)

	// WriteConfig replaces the config.
	WriteConfig(text string) (path string, err error)

	// WriteConfigDraft writes a proposed config without overwriting one.
	WriteConfigDraft(text string) (path string, err error)

	// WriteOutput writes a rendered artifact, returning its path.
	WriteOutput(name string, data []byte) (path string, err error)
}
