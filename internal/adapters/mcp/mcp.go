// Package mcp serves the live graph to coding agents over the Model Context
// Protocol, on stdio.
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/smarty/treaty/internal/app"
)

const protocolVersion = "2025-06-18"

// Server answers MCP requests from the live graph, which stays current as
// files change.
type Server struct {
	live    *app.Live
	version string
	mapURL  string
	shared  bool
}

// request is one JSON-RPC message from the client.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is one JSON-RPC reply.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// tool describes one MCP tool.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// New creates an MCP server over the live graph.
//
// Parameters:
//   - live: the live graph.
//   - version: the treaty version reported to clients.
//   - mapURL: where the person can watch the live map.
//   - shared: the map belongs to a treaty server that was already running
//     in this directory, rather than to this one.
//
// Returns:
//   - result: the server.
func New(live *app.Live, version, mapURL string, shared bool) *Server {
	return &Server{live: live, version: version, mapURL: mapURL, shared: shared}
}

// Serve reads newline-delimited JSON-RPC requests until in closes.
//
// Parameters:
//   - in: the client's messages.
//   - out: where replies are written.
//
// Returns:
//   - err: reading or writing failed.
func (this *Server) Serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var message request
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			if err := encoder.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}); err != nil {
				return err
			}

			continue
		}

		if len(message.ID) == 0 {
			continue
		}

		result, failure := this.handle(message)
		if err := encoder.Encode(response{JSONRPC: "2.0", ID: message.ID, Result: result, Error: failure}); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func (this *Server) call(params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}

	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{-32602, "invalid params"}
	}

	arguments := call.Arguments
	var value any
	var err error
	switch call.Name {
	case "overview":
		var text string
		text, err = this.live.Overview()
		value = "live map: " + this.mapURL + "\n" + text
	case "find":
		value, err = this.live.Find(arguments["query"], arguments["kind"])
	case "slice":
		value, err = this.live.Slice(arguments["target"])
	case "impact":
		value, err = this.live.Impact(arguments["target"])
	case "allowed":
		value, err = this.live.Allowed(arguments["from"], arguments["to"])
	case "changes":
		value, err = this.live.Changes()
	case "selection":
		selection, slice, failure := this.live.Selected()
		value, err = map[string]any{"selection": selection, "slice": slice}, failure
		if failure == nil && selection.Type == "" {
			value = "nothing is selected on the map"
		}
	case "show":
		value, err = "offered on the map: the person sees a notice and decides whether to look", this.live.Show(arguments["target"], arguments["reason"])
		if errors.Is(err, app.ErrTooSoon) {
			value, err = err.Error(), nil
		}
	case "plan":
		value, err = this.live.Plan(arguments["name"], arguments["cml"])
	case "check":
		value, err = this.live.Check()
	case "design_check":
		value, err = this.live.DesignCheck(arguments["name"])
	default:
		return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q", call.Name)}
	}

	if err != nil {
		return map[string]any{"content": []any{map[string]string{"type": "text", "text": err.Error()}}, "isError": true}, nil
	}

	text, ok := value.(string)
	if !ok {
		data, _ := json.MarshalIndent(value, "", "  ")
		text = string(data)
	}

	return map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}}, nil
}

func (this *Server) handle(message request) (any, *rpcError) {
	switch message.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}

		_ = json.Unmarshal(message.Params, &params)
		version := protocolVersion
		if params.ProtocolVersion != "" {
			version = params.ProtocolVersion
		}

		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "treaty", "version": this.version},
			"instructions":    instructions(this.mapURL, this.shared),
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		return this.call(message.Params)
	default:
		return nil, &rpcError{-32601, "method not found: " + message.Method}
	}
}

func schema(properties map[string]string, required ...string) map[string]any {
	props := map[string]any{}
	for name, description := range properties {
		props[name] = map[string]string{"type": "string", "description": description}
	}

	result := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		result["required"] = required
	}

	return result
}

func instructions(mapURL string, shared bool) string {
	where := "This session started treaty's live map at " + mapURL + "."
	if shared {
		where = "A treaty server was already running in this directory, so this session shares its live map at " + mapURL + "."
	}

	return where + " The person cannot see this server's output, so in your first reply of the session give them that link, " +
		"saying whether it is a new map or the one already running, so they can open it in a browser. " +
		"Treaty keeps a live graph of this repository's contracts, placed in hexagonal layers, and rebuilds it as files change. " +
		"Use it to decide where to work without reading the whole codebase: call overview first, find to locate symbols, " +
		"slice before editing a symbol or module, impact before changing a contract, and allowed before adding an import. " +
		"Write what you intend to build as CML with plan; it shows on the person's map and fills in as the code is written. " +
		"After edits, call changes to see what moved in the architecture since the baseline, and fix any new layer violation. " +
		"Call selection to see what the person has selected on the map. Use show sparingly, only when you need the person to look at one specific thing."
}

func tools() []tool {
	return []tool{
		{
			Name:        "overview",
			Description: "The whole architecture in a few kilobytes: every module by layer with its contract count and stability, every module dependency, and violations. Call this first instead of exploring files.",
			InputSchema: schema(nil),
		},
		{
			Name:        "find",
			Description: "Find symbols whose id contains the query, ignoring case. Returns id, kind, file:line and signature, contracts first.",
			InputSchema: schema(map[string]string{"query": "Text to look for, such as Order or graph:Graph.", "kind": "Optional: function, method, interface, type or value."}, "query"),
		},
		{
			Name:        "slice",
			Description: "The minimum context for working on one symbol or module: its contract, its direct neighbors' contracts and the layers it may depend on.",
			InputSchema: schema(map[string]string{"target": "A symbol id such as go:internal/graph:Graph.Order, or a module id such as go:internal/graph."}, "target"),
		},
		{
			Name:        "impact",
			Description: "Everything that depends on a symbol or module, transitively, grouped by module: what an edit to it can break. Call before changing a contract.",
			InputSchema: schema(map[string]string{"target": "A symbol id or module id."}, "target"),
		},
		{
			Name:        "allowed",
			Description: "Whether one module may depend on another under the layer rules, and which layers the first may use. Call before adding an import. Modules that do not exist yet are placed by the config.",
			InputSchema: schema(map[string]string{"from": "The depending module, as an id such as go:internal/app or a path.", "to": "The module depended on, as an id or a path."}, "from", "to"),
		},
		{
			Name:        "changes",
			Description: "How the architecture differs from the baseline the person chose: new and fixed layer violations, contract changes, and module dependencies added and removed. Call after edits to check your work.",
			InputSchema: schema(nil),
		},
		{
			Name:        "selection",
			Description: "What the person has selected on the live map, with its context slice. Use it when the person points at something on the map.",
			InputSchema: schema(nil),
		},
		{
			Name: "show",
			Description: "Ask the person to look at one symbol or module on their live map. It does not move their view: the item pulses and a notice with your reason lets them jump to it, unless they chose to follow you. " +
				"Use it only when you need their eyes on one specific thing, such as before asking them about it or when something needs their decision. Do not use it to narrate progress. " +
				"At most one request per 15 seconds; a request sooner is refused.",
			InputSchema: schema(map[string]string{"target": "A symbol id or module id.", "reason": "One short sentence: why they should look."}, "target", "reason"),
		},
		{
			Name: "plan",
			Description: "Save what you intend to build as unimplemented CML, in design syntax, under .treaty/designs/<name>.cml, replacing any design of that name. " +
				"The plan shows on the person's map in teal and each item fills in as it is built. Returns what is not built yet, what differs from existing code, and layer problems. " +
				"Syntax: file or module headers such as go:internal/app/ports.go or go:internal/adapters/mutation; native declarations indented two spaces, methods and fields under their type, no receivers; " +
				"@depends <module or symbol id> to declare a planned dependency; @forbid <glob>; @expect instability|abstractness|distance >=|<= <number>.",
			InputSchema: schema(map[string]string{"name": "The plan name: letters, digits, dots, dashes and underscores.", "cml": "The plan in CML design syntax. The cml 1 and design lines are added when missing."}, "name", "cml"),
		},
		{
			Name:        "check",
			Description: "Every check on the live graph against the baseline: layer rules, stability metrics, contract changes and the ranked review queue, as JSON.",
			InputSchema: schema(nil),
		},
		{
			Name:        "design_check",
			Description: "Compare a design or plan in .treaty/designs with the code: missing and differing contracts, layer problems and failing expectations.",
			InputSchema: schema(map[string]string{"name": "The design name, without .cml."}, "name"),
		},
	}
}
