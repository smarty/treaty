package web

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const dialTimeout = 2 * time.Second

var (
	ErrNoServer  = errors.New("no treaty server answers")
	ErrNotTreaty = errors.New("something other than treaty answers")
	ErrProtocol  = errors.New("the treaty server speaks another protocol")
	ErrRefused   = errors.New("the treaty server refused the session")
)

// Session is one agent session's MCP stream to the server: what is written
// goes to the server and what is read comes from it.
type Session struct {
	conn   net.Conn
	reader *bufio.Reader

	// Map is where the person sees the session's project.
	Map string

	// Joined is true when the project was already open for another session.
	Joined bool
}

// Attach opens an agent session's MCP stream to the server.
//
// Notes:
//   - The server builds the project's graph before it answers the first
//     session in a directory, so this waits as long as that takes.
//
// Parameters:
//   - base: the server's base URL, such as http://127.0.0.1:7878.
//   - root: the session's directory, absolute.
//
// Returns:
//   - result: the open stream.
//   - err: the session could not be opened.
//
// Errors:
//   - ErrNoServer: nothing listens at base.
//   - ErrNotTreaty: something that is not treaty listens there.
//   - ErrProtocol: the server is a treaty of another protocol.
//   - ErrRefused: the server refused, such as when the graph failed to build.
func Attach(base, root string) (result *Session, err error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout("tcp", parsed.Host, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	request, err := http.NewRequest(http.MethodPost, base+"/api/attach?root="+url.QueryEscape(root), nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", Upgrade)
	request.Header.Set(protocolHeader, strconv.Itoa(Protocol))
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w at %s: %v", ErrNotTreaty, base, err)
	}

	if response.StatusCode == http.StatusSwitchingProtocols && strings.EqualFold(response.Header.Get("Upgrade"), Upgrade) {
		joined, _ := strconv.ParseBool(response.Header.Get(joinedHeader))
		return &Session{conn: conn, reader: reader, Map: response.Header.Get(mapHeader), Joined: joined}, nil
	}

	message, _ := io.ReadAll(io.LimitReader(response.Body, maxBody))
	_ = conn.Close()
	switch {
	case response.StatusCode == http.StatusConflict:
		return nil, fmt.Errorf("%w: %s", ErrProtocol, strings.TrimSpace(string(message)))
	case response.StatusCode == http.StatusInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(string(message)))
	default:
		return nil, fmt.Errorf("%w at %s: %s", ErrNotTreaty, base, response.Status)
	}
}

// Close ends the session.
//
// Returns:
//   - err: the connection could not be closed.
func (this *Session) Close() error {
	return this.conn.Close()
}

// Read reads what the server sent.
//
// Parameters:
//   - data: the buffer to fill.
//
// Returns:
//   - n: the bytes read.
//   - err: the stream ended.
func (this *Session) Read(data []byte) (n int, err error) {
	return this.reader.Read(data)
}

// Write sends to the server.
//
// Parameters:
//   - data: the bytes to send.
//
// Returns:
//   - n: the bytes written.
//   - err: the stream ended.
func (this *Session) Write(data []byte) (n int, err error) {
	return this.conn.Write(data)
}

// FindMap finds the live map of a directory's project on the server.
//
// Parameters:
//   - base: the server's base URL.
//   - root: the directory, absolute.
//
// Returns:
//   - result: the map's URL, empty when the server has no project there.
//   - err: no treaty server answers.
func FindMap(base, root string) (result string, err error) {
	client := &http.Client{Timeout: dialTimeout}
	response, err := client.Get(base + "/api/projects?root=" + url.QueryEscape(root))
	if err != nil {
		return "", fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	defer func() { _ = response.Body.Close() }()
	var projects []projectInfo
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&projects) != nil {
		return "", fmt.Errorf("%w at %s", ErrNotTreaty, base)
	}

	if len(projects) == 0 {
		return "", nil
	}

	return projects[0].URL, nil
}

// OpenedProject is a project opened for the person: where its map is, its
// URL segment, and whether this opened it rather than finding it open.
type OpenedProject struct {
	URL    string `json:"url"`
	Slug   string `json:"slug"`
	Opened bool   `json:"opened"`
}

// Open keeps a directory's project open on the server for the person,
// without an agent session, until they close its tab.
//
// Parameters:
//   - base: the server's base URL.
//   - root: the directory, absolute.
//
// Returns:
//   - result: the project.
//   - err: the project could not be opened.
//
// Errors:
//   - ErrNoServer: nothing listens at base.
//   - ErrProtocol: the server is a treaty of another protocol.
//   - ErrRefused: the server refused, such as when the graph failed to build.
func Open(base, root string) (result OpenedProject, err error) {
	data, _ := json.Marshal(map[string]string{"root": root})
	request, err := http.NewRequest(http.MethodPost, base+"/api/open", strings.NewReader(string(data)))
	if err != nil {
		return result, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(protocolHeader, strconv.Itoa(Protocol))
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return result, fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusConflict, response.StatusCode == http.StatusNotFound:
		return result, fmt.Errorf("%w at %s", ErrProtocol, base)
	case response.StatusCode != http.StatusOK:
		text, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return result, fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(string(text)))
	}

	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("%w at %s", ErrNotTreaty, base)
	}

	return result, nil
}

// Close closes a project the person kept open.
//
// Parameters:
//   - base: the server's base URL.
//   - slug: the project's URL segment.
//
// Returns:
//   - err: no treaty server answered, or it refused.
func Close(base, slug string) error {
	data, _ := json.Marshal(map[string]string{"slug": slug})
	response, err := (&http.Client{Timeout: dialTimeout}).Post(base+"/api/close", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		text, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(string(text)))
	}

	return nil
}

// Pages reports whether a page shows the server's projects, so a process
// that opens one knows whether a browser is needed.
//
// Parameters:
//   - base: the server's base URL.
//
// Returns:
//   - shown: a page asked for the projects in the last few seconds.
//   - err: no treaty server answered.
func Pages(base string) (shown bool, err error) {
	response, err := (&http.Client{Timeout: dialTimeout}).Get(base + "/api/hello")
	if err != nil {
		return false, fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	defer func() { _ = response.Body.Close() }()
	var answer hello
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&answer) != nil || answer.App != "treaty" {
		return false, fmt.Errorf("%w at %s", ErrNotTreaty, base)
	}

	return answer.Pages, nil
}

// Stop asks the server to stop. Its sessions' shims start a new one, which
// is how a newly built treaty takes over.
//
// Parameters:
//   - base: the server's base URL.
//
// Returns:
//   - err: no treaty server answered.
func Stop(base string) error {
	client := &http.Client{Timeout: dialTimeout}
	response, err := client.Post(base+"/api/shutdown", "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrNoServer, base, err)
	}

	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w at %s: %s", ErrNotTreaty, base, response.Status)
	}

	return nil
}
