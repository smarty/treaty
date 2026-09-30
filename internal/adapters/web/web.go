// Package web serves the live map to browsers over HTTP on the loopback
// interface, pushing each rebuild with server-sent events.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/smarty/treaty/internal/app"
)

const (
	heartbeat = 20 * time.Second
	maxBody   = 64 * 1024
)

var ErrPeer = errors.New("the peer did not answer")

// Peer reads the state of another treaty server over HTTP.
type Peer struct {
	url    string
	client *http.Client
}

// Server serves one live map.
type Server struct {
	live     *app.Live
	listener net.Listener
	server   *http.Server
	url      string
}

// Listen starts serving the live map on 127.0.0.1.
//
// Parameters:
//   - live: the live graph to serve.
//   - port: the port to listen on; if it is taken, any free port is used.
//
// Returns:
//   - result: the running server.
//   - err: no port could be opened.
func Listen(live *app.Live, port int) (result *Server, err error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if listener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}

	result = &Server{live: live, listener: listener, url: "http://" + listener.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", result.page)
	mux.HandleFunc("GET /api/view", result.view)
	mux.HandleFunc("GET /api/state", result.state)
	mux.HandleFunc("GET /api/events", result.events)
	mux.HandleFunc("POST /api/baseline", result.baseline)
	mux.HandleFunc("POST /api/selection", result.selection)
	mux.HandleFunc("POST /api/show", result.show)
	mux.HandleFunc("GET /api/preferences", result.preferences)
	mux.HandleFunc("POST /api/preferences", result.savePreferences)
	mux.HandleFunc("POST /api/view", result.setView)
	mux.HandleFunc("POST /api/view/adopt", result.adopt)
	result.server = &http.Server{Handler: result.guard(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = result.server.Serve(listener) }()
	return result, nil
}

// NewPeer creates a client for another treaty server.
//
// Parameters:
//   - url: the server's base URL.
//
// Returns:
//   - result: the peer.
func NewPeer(url string) *Peer {
	return &Peer{url: strings.TrimSuffix(url, "/"), client: &http.Client{Timeout: 2 * time.Second}}
}

// Alive reports whether a treaty server answers at a URL.
//
// Parameters:
//   - url: the server's base URL.
//
// Returns:
//   - result: true when it answered.
func (this *Peer) Alive() bool {
	_, err := this.State()
	return err == nil
}

// Show forwards a request for the person to look at something to the peer,
// which applies its own rate limit.
//
// Parameters:
//   - target: a symbol id or module id.
//   - reason: why the person should look.
//
// Returns:
//   - err: the peer refused or did not answer; the message is the peer's.
func (this *Peer) Show(target, reason string) error {
	body, _ := json.Marshal(map[string]string{"target": target, "reason": reason})
	response, err := this.client.Post(this.url+"/api/show", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}

	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusOK {
		return nil
	}

	message, _ := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if response.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w%s", app.ErrTooSoon, strings.TrimPrefix(strings.TrimSpace(string(message)), app.ErrTooSoon.Error()))
	}

	return fmt.Errorf("%w: %s", ErrPeer, strings.TrimSpace(string(message)))
}

// State reads the peer's baseline and selection.
//
// Returns:
//   - result: the peer's state.
//   - err: the peer did not answer.
//
// Errors:
//   - ErrPeer: the peer answered with something other than a state.
func (this *Peer) State() (result app.LiveState, err error) {
	response, err := this.client.Get(this.url + "/api/state")
	if err != nil {
		return app.LiveState{}, err
	}

	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return app.LiveState{}, fmt.Errorf("%w: %s", ErrPeer, response.Status)
	}

	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

// Close stops serving.
//
// Returns:
//   - err: the listener could not be closed.
func (this *Server) Close() error {
	return this.server.Close()
}

// URL is where the live map is served.
//
// Returns:
//   - result: the base URL.
func (this *Server) URL() string {
	return this.url
}

// adopt makes the previewed architecture treaty.yaml's.
func (this *Server) adopt(writer http.ResponseWriter, request *http.Request) {
	var ignored struct{}
	if !decode(writer, request, &ignored) {
		return
	}

	if _, err := this.live.AdoptView(); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(writer, request)
}

func (this *Server) baseline(writer http.ResponseWriter, request *http.Request) {
	var baseline app.Baseline
	if !decode(writer, request, &baseline) {
		return
	}

	if err := this.live.SetBaseline(baseline); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(writer, request)
}

// events streams the state after every rebuild as server-sent events. The
// browser fetches the view when the version changes.
func (this *Server) events(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	updates, cancel := this.live.Subscribe()
	defer cancel()
	send := func(state app.LiveState) bool {
		data, _ := json.Marshal(state)
		if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
			return false
		}

		flusher.Flush()
		return true
	}

	if !send(this.live.State()) {
		return
	}

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case state := <-updates:
			if !send(state) {
				return
			}
		case <-ticker.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}

// guard refuses requests addressed to another host name, which is how DNS
// rebinding reaches a loopback server.
func (this *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(this.listener.Addr().String())
	allowed := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !allowed[request.Host] {
			http.Error(writer, "forbidden host", http.StatusForbidden)
			return
		}

		next.ServeHTTP(writer, request)
	})
}

func (this *Server) page(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(this.live.Page())
}

// preferences serves the person's saved choices on the map.
func (this *Server) preferences(writer http.ResponseWriter, _ *http.Request) {
	preferences, err := this.live.Preferences()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, preferences)
}

// savePreferences merges the fields sent into the saved choices.
func (this *Server) savePreferences(writer http.ResponseWriter, request *http.Request) {
	var update app.Preferences
	if !decode(writer, request, &update) {
		return
	}

	preferences, err := this.live.SavePreferences(update)
	if errors.Is(err, app.ErrPreferences) {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	respond(writer, preferences)
}

func (this *Server) selection(writer http.ResponseWriter, request *http.Request) {
	var selection app.Selection
	if !decode(writer, request, &selection) {
		return
	}

	this.live.SetSelection(selection)
	writer.WriteHeader(http.StatusNoContent)
}

// setView draws another architecture, or treaty.yaml's again.
func (this *Server) setView(writer http.ResponseWriter, request *http.Request) {
	var view struct {
		Architecture string `json:"architecture"`
	}

	if !decode(writer, request, &view) {
		return
	}

	if err := this.live.SetView(view.Architecture); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	this.state(writer, request)
}

func (this *Server) show(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Target string `json:"target"`
		Reason string `json:"reason"`
	}

	if !decode(writer, request, &body) {
		return
	}

	switch err := this.live.Show(body.Target, body.Reason); {
	case errors.Is(err, app.ErrTooSoon):
		http.Error(writer, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, app.ErrUnknownTarget):
		http.Error(writer, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(writer, err.Error(), http.StatusBadRequest)
	default:
		this.state(writer, request)
	}
}

func (this *Server) state(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(this.live.State())
}

func (this *Server) view(writer http.ResponseWriter, _ *http.Request) {
	data, version := this.live.Payload()
	if data == nil {
		http.Error(writer, "the map is still being built", http.StatusServiceUnavailable)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Treaty-Version", fmt.Sprint(version))
	_, _ = writer.Write(data)
}

// decode reads a JSON body. Requiring the JSON content type means another
// web page cannot post here without a CORS preflight, which is never
// granted.
func decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	if media, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type")); media != "application/json" {
		http.Error(writer, "expected application/json", http.StatusUnsupportedMediaType)
		return false
	}

	if err := json.NewDecoder(io.LimitReader(request.Body, maxBody)).Decode(target); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return false
	}

	return true
}

// respond writes a value as JSON that no cache keeps.
func respond(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(value)
}
