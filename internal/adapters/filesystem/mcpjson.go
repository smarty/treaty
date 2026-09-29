package filesystem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/smarty/treaty/internal/app"
)

const (
	MCPFile = ".mcp.json"

	serversKey = "mcpServers"
)

var (
	ErrMCPFile    = errors.New(".mcp.json is not a JSON object")
	ErrRegistered = errors.New("a different server is already registered under that name")

	indentation = regexp.MustCompile(`\n([ \t]+)"`)
)

// AgentConfig edits the .mcp.json at the repository root.
type AgentConfig struct {
	root string
}

// object is a JSON object that remembers the order of its keys, so that
// rewriting a person's file changes only what was meant to change.
type object struct {
	keys   []string
	values map[string]json.RawMessage
}

// NewAgentConfig creates the .mcp.json editor for a repository.
//
// Parameters:
//   - root: the repository root.
//
// Returns:
//   - result: the editor.
func NewAgentConfig(root string) *AgentConfig {
	return &AgentConfig{root: root}
}

// Register adds a server to .mcp.json, creating the file when there is none.
// Every other entry, the order of keys and the file's indentation are kept.
//
// Notes:
//   - An existing entry with the same command and arguments counts as
//     unchanged, whatever other settings it has.
//
// Parameters:
//   - name: the server's name.
//   - server: how to start it.
//   - replace: overwrite a different entry with the same name.
//
// Returns:
//   - result: the file's path and created, added, replaced or unchanged.
//   - err: the file could not be read or written, or is not valid.
//
// Errors:
//   - ErrMCPFile: the file, or its mcpServers, is not a JSON object.
//   - ErrRegistered: a different entry has that name and replace is false.
func (this *AgentConfig) Register(name string, server app.MCPServer, replace bool) (result app.Registration, err error) {
	path := filepath.Join(this.root, MCPFile)
	command, _ := json.Marshal(server.Command)
	args, _ := json.Marshal(server.Args)
	fields := &object{values: map[string]json.RawMessage{}}
	fields.set("command", command)
	fields.set("args", args)
	entry := fields.encode()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		servers := &object{values: map[string]json.RawMessage{}}
		servers.set(name, entry)
		top := &object{values: map[string]json.RawMessage{}}
		top.set(serversKey, servers.encode())
		return app.Registration{Path: path, Outcome: app.RegistrationCreated}, write(path, top.encode(), "  ")
	}

	if err != nil {
		return app.Registration{}, err
	}

	top, err := decodeObject(data)
	if err != nil {
		return app.Registration{}, fmt.Errorf("%w: %v", ErrMCPFile, err)
	}

	servers := &object{values: map[string]json.RawMessage{}}
	if raw, ok := top.values[serversKey]; ok {
		if servers, err = decodeObject(raw); err != nil {
			return app.Registration{}, fmt.Errorf("%w: %s: %v", ErrMCPFile, serversKey, err)
		}
	}

	outcome := app.RegistrationAdded
	if existing, ok := servers.values[name]; ok {
		if sameServer(existing, server) {
			return app.Registration{Path: path, Outcome: app.RegistrationUnchanged}, nil
		}

		if !replace {
			return app.Registration{}, fmt.Errorf("%w: %s in %s is %s", ErrRegistered, name, path, bytes.Join(bytes.Fields(existing), []byte(" ")))
		}

		outcome = app.RegistrationReplaced
	}

	servers.set(name, entry)
	top.set(serversKey, servers.encode())
	indent := "  "
	if match := indentation.FindSubmatch(data); match != nil {
		indent = string(match[1])
	}

	return app.Registration{Path: path, Outcome: outcome}, write(path, top.encode(), indent)
}

func (this *object) encode() json.RawMessage {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range this.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}

		name, _ := json.Marshal(key)
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(this.values[key])
	}

	buffer.WriteByte('}')
	return buffer.Bytes()
}

func (this *object) set(key string, value json.RawMessage) {
	if !slices.Contains(this.keys, key) {
		this.keys = append(this.keys, key)
	}

	this.values[key] = value
}

func decodeObject(data []byte) (*object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return nil, errors.New("expected an object")
	}

	result := &object{values: map[string]json.RawMessage{}}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}

		result.set(token.(string), value)
	}

	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	if _, err := decoder.Token(); err == nil {
		return nil, errors.New("unexpected data after the object")
	}

	return result, nil
}

func sameServer(raw json.RawMessage, server app.MCPServer) bool {
	var existing struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}

	return json.Unmarshal(raw, &existing) == nil && existing.Command == server.Command && slices.Equal(existing.Args, server.Args)
}

func write(path string, compact json.RawMessage, indent string) error {
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, compact, "", indent); err != nil {
		return err
	}

	buffer.WriteByte('\n')
	return os.WriteFile(path, buffer.Bytes(), 0o644)
}
