package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	firstRetry = 100 * time.Millisecond
	lastRetry  = time.Second
)

// Shim relays an agent's MCP session on stdio to the shared treaty server.
// When the server goes away, as when a new treaty replaces it, the shim
// connects again, sends the requests that were not answered again, and tells
// the agent the tools may have changed, so the session outlives the server.
type Shim struct {
	connect  func() (io.ReadWriteCloser, error)
	patience time.Duration
}

// pending is a request the server has not answered yet.
type pending struct {
	key    string
	method string
	id     json.RawMessage
	line   []byte
}

// reply is one line from the server, or the end of its stream.
type reply struct {
	generation int
	line       []byte
	err        error
}

// NewShim creates a shim.
//
// Parameters:
//   - connect: opens a session's stream to the server, starting the server
//     when none runs.
//   - patience: how long to keep trying when the server goes away before
//     failing the requests waiting on it.
//
// Returns:
//   - result: the shim.
func NewShim(connect func() (io.ReadWriteCloser, error), patience time.Duration) *Shim {
	return &Shim{connect: connect, patience: patience}
}

// Serve relays newline-delimited JSON-RPC between the agent and the server
// until the agent closes in.
//
// Notes:
//   - A request that waited out the patience is answered with an error the
//     agent can read; the next request tries the server again.
//
// Parameters:
//   - in: the agent's messages.
//   - out: where the server's messages go.
//
// Returns:
//   - err: the server could not be reached at the start, or reading the
//     agent failed.
func (this *Shim) Serve(in io.Reader, out io.Writer) (err error) {
	lines := make(chan []byte)
	var scanErr error
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}

		scanErr = scanner.Err()
	}()

	replies := make(chan reply, 16)
	var conn io.ReadWriteCloser
	var waiting []pending
	generation := 0
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	// open connects within the patience, starts reading the new stream and
	// sends every waiting request again.
	open := func() error {
		deadline := time.Now().Add(this.patience)
		delay := firstRetry
		for {
			next, err := this.connect()
			if err == nil {
				conn = next
				generation++
				go read(conn, generation, replies)
				sent := true
				for _, request := range waiting {
					if _, err := conn.Write(append(request.line, '\n')); err != nil {
						sent = false
						break
					}
				}

				if sent {
					return nil
				}

				_ = conn.Close()
				conn, err = nil, fmt.Errorf("the treaty server closed the session")
			}

			if time.Now().After(deadline) {
				return err
			}

			time.Sleep(delay)
			delay = min(2*delay, lastRetry)
		}
	}

	// reopen replaces a lost stream, failing what waits when it cannot.
	reopen := func() {
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}

		if err := open(); err != nil {
			for _, request := range waiting {
				_ = writeLine(out, failed(request, err))
			}

			waiting = nil
			return
		}

		_ = writeLine(out, []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
	}

	if err := open(); err != nil {
		return err
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return scanErr
			}

			if request, ok := parseRequest(line); ok {
				waiting = append(waiting, request)
			}

			if conn == nil {
				reopen()
				continue
			}

			if _, err := conn.Write(append(line, '\n')); err != nil {
				reopen()
			}
		case next := <-replies:
			if next.generation != generation {
				continue
			}

			if next.err != nil {
				reopen()
				continue
			}

			waiting = answer(waiting, next.line)
			if err := writeLine(out, next.line); err != nil {
				return err
			}
		}
	}
}

// answer drops the request a reply answers from those waiting.
func answer(waiting []pending, line []byte) []pending {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}

	if json.Unmarshal(line, &message) != nil || message.Method != "" || len(message.ID) == 0 {
		return waiting
	}

	for i, request := range waiting {
		if request.key == string(message.ID) {
			return append(waiting[:i], waiting[i+1:]...)
		}
	}

	return waiting
}

// failed answers a request that the server could not, in a form the agent
// shows: a tool result for a tool call, an error for anything else.
func failed(request pending, err error) []byte {
	text := fmt.Sprintf("treaty: the treaty server went away and did not come back (%v); try again, or run /mcp to reconnect", err)
	var data []byte
	if request.method == "tools/call" {
		data, _ = json.Marshal(response{JSONRPC: "2.0", ID: request.id, Result: map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}, "isError": true}})
	} else {
		data, _ = json.Marshal(response{JSONRPC: "2.0", ID: request.id, Error: &rpcError{-32603, text}})
	}

	return data
}

// parseRequest recognizes a request that expects a reply.
func parseRequest(line []byte) (result pending, ok bool) {
	var message request
	if json.Unmarshal(line, &message) != nil || message.Method == "" || len(message.ID) == 0 {
		return pending{}, false
	}

	return pending{key: string(message.ID), method: message.Method, id: message.ID, line: line}, true
}

// read delivers each line of one stream, then its end.
func read(conn io.Reader, generation int, replies chan<- reply) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		replies <- reply{generation: generation, line: append([]byte(nil), scanner.Bytes()...)}
	}

	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}

	replies <- reply{generation: generation, err: err}
}

func writeLine(out io.Writer, line []byte) error {
	_, err := out.Write(append(line, '\n'))
	return err
}
