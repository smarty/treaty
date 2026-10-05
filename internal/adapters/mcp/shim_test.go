package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// servers stands in for the treaty server across restarts: each connection
// is a new server, and a server can be told to drop its connection on the
// next tool call, as one that restarts does.
type servers struct {
	mutex   sync.Mutex
	started int
	drop    map[int]bool
	down    bool
}

func TestShimRejoinsARestartedServer(t *testing.T) {
	fake := &servers{drop: map[int]bool{1: true}}
	agent, replies := shim(t, fake, time.Second)

	send(t, agent, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if got := next(t, replies); !strings.Contains(got, `"id":1`) || !strings.Contains(got, `"server":1`) {
		t.Fatalf("initialize: %s", got)
	}

	// The first server goes away while it holds a tool call; the shim starts
	// over with the next, sends the call again and says the tools may have
	// changed.
	send(t, agent, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"overview"}}`)
	if got := next(t, replies); !strings.Contains(got, "notifications/tools/list_changed") {
		t.Fatalf("the agent must hear the tools may have changed: %s", got)
	}

	if got := next(t, replies); !strings.Contains(got, `"id":2`) || !strings.Contains(got, `"server":2`) {
		t.Fatalf("the call must be answered by the new server: %s", got)
	}

	send(t, agent, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(t, agent, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if got := next(t, replies); !strings.Contains(got, `"id":3`) || !strings.Contains(got, `"server":2`) {
		t.Fatalf("later calls go to the new server: %s", got)
	}
}

func TestShimFailsWhatWaitsWhenNoServerComesBack(t *testing.T) {
	fake := &servers{drop: map[int]bool{1: true}}
	agent, replies := shim(t, fake, 200*time.Millisecond)
	send(t, agent, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	next(t, replies)

	fake.mutex.Lock()
	fake.down = true
	fake.mutex.Unlock()
	send(t, agent, `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"overview"}}`)
	var result struct {
		ID     string `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}

	got := next(t, replies)
	if err := json.Unmarshal([]byte(got), &result); err != nil || result.ID != "call" || !result.Result.IsError || !strings.Contains(result.Result.Content[0].Text, "/mcp") {
		t.Fatalf("a tool call that cannot be answered must say so: %s", got)
	}

	// The next request tries again, and finds a server once one is back.
	fake.mutex.Lock()
	fake.down = false
	fake.mutex.Unlock()
	send(t, agent, `{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if got := next(t, replies); !strings.Contains(got, "list_changed") {
		t.Fatalf("rejoining: %s", got)
	}

	if got := next(t, replies); !strings.Contains(got, `"id":4`) {
		t.Fatalf("ping after rejoining: %s", got)
	}
}

func TestShimNeedsAServerToStart(t *testing.T) {
	fake := &servers{down: true}
	shim := NewShim(fake.connect, 50*time.Millisecond)
	if err := shim.Serve(strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("a shim with no server must fail")
	}
}

func (this *servers) connect() (io.ReadWriteCloser, error) {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	if this.down {
		return nil, errors.New("connection refused")
	}

	this.started++
	number, drop := this.started, this.drop[this.started]
	near, far := net.Pipe()
	go func() {
		defer func() { _ = far.Close() }()
		scanner := bufio.NewScanner(far)
		for scanner.Scan() {
			var message request
			_ = json.Unmarshal(scanner.Bytes(), &message)
			if len(message.ID) == 0 {
				continue
			}

			if drop && message.Method == "tools/call" {
				return
			}

			if _, err := fmt.Fprintf(far, `{"jsonrpc":"2.0","id":%s,"result":{"server":%d}}`+"\n", message.ID, number); err != nil {
				return
			}
		}
	}()

	return near, nil
}

func next(t *testing.T, replies <-chan string) string {
	t.Helper()
	select {
	case line := <-replies:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
		return ""
	}
}

func send(t *testing.T, agent io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(agent, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

// shim runs a shim against the fake servers, returning what the agent
// writes to and each line it reads back.
func shim(t *testing.T, fake *servers, patience time.Duration) (agent io.Writer, replies <-chan string) {
	t.Helper()
	in, agentWriter := io.Pipe()
	agentReader, out := io.Pipe()
	lines := make(chan string, 16)
	go func() { _ = NewShim(fake.connect, patience).Serve(in, out) }()
	go func() {
		scanner := bufio.NewScanner(agentReader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	t.Cleanup(func() { _ = agentWriter.Close(); _ = agentReader.Close() })
	return agentWriter, lines
}
