// Command treaty is Treaty: it reads a codebase into a contract graph and
// checks, slices and draws it. This is the composition root.
//
// One treaty server runs on this machine at a fixed port and serves the live
// map of every repository a session works in. Each agent session runs
// treaty mcp, a shim that relays its MCP stream to that server, starting
// the server when none runs and rejoining it when it restarts.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/smarty/treaty/internal/adapters/cli"
	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/adapters/gitvcs"
	"github.com/smarty/treaty/internal/adapters/htmlmap"
	"github.com/smarty/treaty/internal/adapters/language/golang"
	"github.com/smarty/treaty/internal/adapters/language/javascript"
	"github.com/smarty/treaty/internal/adapters/language/python"
	"github.com/smarty/treaty/internal/adapters/mcp"
	"github.com/smarty/treaty/internal/adapters/web"
	"github.com/smarty/treaty/internal/app"
)

const (
	version = "0.2.0"

	// patience is how long a session waits for the treaty server to come
	// back before failing the requests it sent.
	patience = 15 * time.Second

	// startup is how long a session waits for a treaty server it started to
	// answer.
	startup = 10 * time.Second

	// maxLog is how large ~/.treaty/daemon.log grows before a new server
	// starts it over.
	maxLog = 1024 * 1024
)

// launcher reaches the one treaty server on this machine, for the
// repository at root.
type launcher struct {
	root string
}

// sessions answers each agent session's MCP requests from its project's
// live graph.
type sessions struct{}

func main() {
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "treaty: %v\n", err)
		os.Exit(2)
	}

	root, args, err := cli.Directory(os.Args[1:], workingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "treaty: %v\n", err)
		os.Exit(2)
	}

	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	service, _ := build(root)
	os.Exit(cli.Run(args, service, launcher{root: root}, os.Stdin, os.Stdout, os.Stderr))
}

// Daemon is the treaty server: it serves every open project until no
// session has been open for app.ProjectGrace, someone runs treaty restart,
// or the treaty executable is replaced, which lets a newly built treaty
// take over.
func (this launcher) Daemon(port int, stderr io.Writer) error {
	projects := app.NewProjects(open, app.ProjectGrace)
	server, err := web.Listen(projects, sessions{}, version, port)
	if err != nil {
		return fmt.Errorf("the treaty server cannot listen on port %d: %w", port, err)
	}

	defer func() { _ = server.Close() }()
	fmt.Fprintf(stderr, "treaty: %s server %d serving at %s\n", time.Now().Format(time.DateTime), os.Getpid(), server.URL())
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	reason := "interrupted"
	select {
	case <-projects.Idle():
		reason = "no session is open"
	case <-server.Stopped():
		reason = "asked to stop"
	case <-replaced():
		reason = "treaty was rebuilt"
	case <-interrupted:
	}

	fmt.Fprintf(stderr, "treaty: %s server %d stopping: %s\n", time.Now().Format(time.DateTime), os.Getpid(), reason)
	return nil
}

// MCP relays an agent session to the treaty server, starting it when none
// runs, and keeps the session going across restarts of the server.
func (this launcher) MCP(port int, in io.Reader, out, stderr io.Writer) error {
	connect := this.connect(port, func(session *web.Session) {
		fmt.Fprintf(stderr, "treaty: live map at %s\n", session.Map)
	})

	return mcp.NewShim(connect, patience).Serve(in, out)
}

// Restart stops the treaty server; its sessions start a new one and rejoin.
func (this launcher) Restart(port int, stderr io.Writer) error {
	base := baseURL(port)
	if err := web.Stop(base); errors.Is(err, web.ErrNoServer) {
		fmt.Fprintln(stderr, "treaty: no treaty server is running")
		return nil
	} else if err != nil {
		return err
	}

	fmt.Fprintln(stderr, "treaty: stopped the treaty server; open sessions start a new one and rejoin")
	return nil
}

// Serve keeps this directory's live map open until interrupted, as a
// session that sends nothing.
func (this launcher) Serve(port int, open bool, stderr io.Writer) error {
	first := true
	connect := this.connect(port, func(session *web.Session) {
		if !first {
			fmt.Fprintln(stderr, "treaty: rejoined the treaty server")
			return
		}

		first = false
		fmt.Fprintf(stderr, "treaty: live map at %s (Ctrl-C to stop)\n", session.Map)
		if open {
			browse(session.Map)
		}
	})

	in, done := io.Pipe()
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interrupted
		_ = done.Close()
	}()

	return mcp.NewShim(connect, patience).Serve(in, io.Discard)
}

// URL finds this directory's live map.
func (this launcher) URL(port int) (url string, err error) {
	url, err = web.FindMap(baseURL(port), this.root)
	if errors.Is(err, web.ErrNoServer) {
		return "", nil
	}

	return url, err
}

// connect opens this directory's session to the treaty server, starting
// the server when none answers, or when one of another protocol does.
func (this launcher) connect(port int, attached func(*web.Session)) func() (io.ReadWriteCloser, error) {
	base := baseURL(port)
	return func() (io.ReadWriteCloser, error) {
		session, err := web.Attach(base, this.root)
		if errors.Is(err, web.ErrProtocol) {
			_ = web.Stop(base)
			session, err = this.await(base)
		}

		if errors.Is(err, web.ErrNoServer) {
			if err := spawn(port); err != nil {
				return nil, err
			}

			session, err = this.await(base)
		}

		if err != nil {
			return nil, err
		}

		attached(session)
		return session, nil
	}
}

// await attaches once a treaty server answers, within startup.
func (this launcher) await(base string) (session *web.Session, err error) {
	deadline := time.Now().Add(startup)
	for {
		time.Sleep(100 * time.Millisecond)
		session, err = web.Attach(base, this.root)
		if !errors.Is(err, web.ErrNoServer) && !errors.Is(err, web.ErrProtocol) || time.Now().After(deadline) {
			return session, err
		}
	}
}

func (sessions) Serve(project *app.Project, mapURL string, joined bool, in io.Reader, out io.Writer) error {
	return mcp.New(project.Live, version, mapURL, joined).Serve(in, out)
}

func baseURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func browse(url string) {
	command := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command = "explorer"
	}

	_ = exec.Command(command, url).Start()
}

// build wires the use cases of the repository at root, and its tests.
func build(root string) (*app.Service, *app.Tests) {
	extractors := app.Extractors{golang.NewExtractor(), javascript.NewExtractor(), python.NewExtractor()}
	service := app.NewService(
		root,
		filesystem.NewConfig(root),
		extractors,
		[]app.Dialect{golang.NewDialect(), javascript.NewDialect(javascript.LanguageJavaScript), javascript.NewDialect(javascript.LanguageTypeScript), python.NewDialect()},
		gitvcs.New(root),
		filesystem.NewWorkspace(root),
		htmlmap.New(),
		filesystem.NewAgentConfig(root),
		filesystem.NewThemes(treatyHome("themes")),
		filesystem.NewPreferences(treatyHome("settings.json")),
	)

	suites := []app.TestSuite{golang.NewTestSuite()}
	service.UseTestSuites(suites...)
	return service, app.NewTests(root, extractors, suites)
}

// open builds and starts the live graph of the repository at root, for the
// treaty server.
func open(root string) (*app.Live, func(), error) {
	if info, err := os.Stat(root); err != nil {
		return nil, nil, err
	} else if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", root)
	}

	service, tests := build(root)
	live := app.NewLive(service, filesystem.NewWatcher(root))
	live.UseTests(tests)
	stop := make(chan struct{})
	live.Start(stop)
	return live, func() { close(stop) }, nil
}

// replaced closes when the treaty executable is replaced, as go install
// does, so the server stops and its sessions start the new one.
func replaced() <-chan struct{} {
	result := make(chan struct{})
	path, err := os.Executable()
	if err != nil {
		return result
	}

	before, err := os.Stat(path)
	if err != nil {
		return result
	}

	go func() {
		for range time.Tick(2 * time.Second) {
			if now, err := os.Stat(path); err == nil && (!os.SameFile(before, now) || !now.ModTime().Equal(before.ModTime())) {
				close(result)
				return
			}
		}
	}()

	return result
}

// spawn starts the treaty server apart from this session, so it outlives
// it; it logs to ~/.treaty/daemon.log. Several sessions may start one at
// once: the first to take the port serves and the rest exit.
func spawn(port int) error {
	path, err := os.Executable()
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}

	if name := treatyHome("daemon.log"); name != "" {
		_ = os.MkdirAll(filepath.Dir(name), 0o755)
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if info, err := os.Stat(name); err == nil && info.Size() > maxLog {
			flags |= os.O_TRUNC
		}

		if file, err := os.OpenFile(name, flags, 0o644); err == nil {
			_ = logFile.Close()
			logFile = file
		}
	}

	defer func() { _ = logFile.Close() }()
	command := exec.Command(path, "daemon", "--port", strconv.Itoa(port))
	command.Dir = os.TempDir()
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = detached()
	if err := command.Start(); err != nil {
		return err
	}

	// Waiting reaps the server if it stops while this session runs.
	go func() { _ = command.Wait() }()
	return nil
}

// treatyHome names a path in ~/.treaty, where a person's themes and
// preferences live across repositories; empty when there is no home
// directory, which keeps the built-in themes and saves nothing.
func treatyHome(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".treaty", name)
}
