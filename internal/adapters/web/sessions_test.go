package web

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/app"
)

// echo answers each line with its project's name, joined flag and the line.
type echo struct{}

func TestSessions(t *testing.T) {
	projects := app.NewProjects(func(string) (*app.Live, func(), error) { return app.NewLive(nil, nil), func() {}, nil }, 50*time.Millisecond)
	server, err := Listen(projects, echo{}, "test", 0)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()
	if got := get(t, server.URL()+"/api/hello", http.StatusOK); !strings.Contains(string(got), `"app":"treaty"`) {
		t.Fatalf("hello: %s", got)
	}

	if page := get(t, server.URL()+"/", http.StatusOK); !strings.Contains(string(page), "No project is open") {
		t.Fatalf("home with nothing open: %s", page)
	}

	root := filepath.Join(t.TempDir(), "shop")
	write(t, root, "go.mod", "module example.com/shop\n")
	first, err := Attach(server.URL(), root)
	if err != nil || first.Joined || first.Map != server.URL()+"/?p=shop" {
		t.Fatalf("first: %+v %v", first, err)
	}

	defer func() { _ = first.Close() }()
	if got := roundTrip(t, first, "ping"); got != "shop false ping" {
		t.Fatalf("first session: %q", got)
	}

	// A second session in the same directory joins the open project.
	second, err := Attach(server.URL(), root)
	if err != nil || !second.Joined {
		t.Fatalf("second: %+v %v", second, err)
	}

	if got := roundTrip(t, second, "pong"); got != "shop true pong" {
		t.Fatalf("second session: %q", got)
	}

	if found, err := FindMap(server.URL(), root); err != nil || found != first.Map {
		t.Fatalf("find map: %q %v", found, err)
	}

	if found, err := FindMap(server.URL(), t.TempDir()); err != nil || found != "" {
		t.Fatalf("a directory with nothing open: %q %v", found, err)
	}

	if page := get(t, server.URL()+"/", http.StatusOK); !strings.Contains(string(page), `const LATEST = "shop"`) {
		t.Fatalf("home must show the latest project: %s", page)
	}

	get(t, server.URL()+"/p/elsewhere/api/state", http.StatusNotFound)

	// A session leaves its project when its stream ends, however it ends.
	_ = second.Close()
	waitFor(t, func() bool { return sessions(projects) == 1 })
	_ = first.Close()
	waitFor(t, func() bool { return len(projects.List()) == 0 })

	if err := Stop(server.URL()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-server.Stopped():
	case <-time.After(time.Second):
		t.Fatal("stop did not reach the server")
	}
}

func TestAttachRefusals(t *testing.T) {
	projects := app.NewProjects(func(string) (*app.Live, func(), error) { return nil, nil, errors.New("no graph") }, time.Minute)
	server, err := Listen(projects, echo{}, "test", 0)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = server.Close() }()
	if _, err := Attach(server.URL(), t.TempDir()); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no graph") {
		t.Fatalf("a project that cannot open: %v", err)
	}

	upgrade := func(protocol, root string) int {
		request, _ := http.NewRequest(http.MethodPost, server.URL()+"/api/attach?root="+root, nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", Upgrade)
		request.Header.Set(protocolHeader, protocol)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		_ = response.Body.Close()
		return response.StatusCode
	}

	if status := upgrade("99", t.TempDir()); status != http.StatusConflict {
		t.Fatalf("another protocol: %d", status)
	}

	if status := upgrade(fmt.Sprint(Protocol), "relative"); status != http.StatusBadRequest {
		t.Fatalf("a relative root: %d", status)
	}

	post(t, server.URL()+"/api/attach", "application/json", `{}`, http.StatusUpgradeRequired)
	post(t, server.URL()+"/api/shutdown", "text/plain", `{}`, http.StatusUnsupportedMediaType)

	// Nothing listening, and something else listening, are told apart.
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	address := listener.Addr().String()
	_ = listener.Close()
	if _, err := Attach("http://"+address, t.TempDir()); !errors.Is(err, ErrNoServer) {
		t.Fatalf("nothing listening: %v", err)
	}

	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if _, err := Attach(other.URL, t.TempDir()); !errors.Is(err, ErrNotTreaty) {
		t.Fatalf("something else listening: %v", err)
	}
}

func (echo) Serve(project *app.Project, _ string, joined bool, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		if _, err := fmt.Fprintf(out, "%s %t %s\n", project.Name, joined, scanner.Text()); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func roundTrip(t *testing.T, session *Session, line string) string {
	t.Helper()
	if _, err := io.WriteString(session, line+"\n"); err != nil {
		t.Fatal(err)
	}

	reply, err := bufio.NewReader(session).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSuffix(reply, "\n")
}

func sessions(projects *app.Projects) (result int) {
	for _, project := range projects.List() {
		result += project.Sessions
	}

	return result
}
