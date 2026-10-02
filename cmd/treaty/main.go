// Command treaty is Treaty: it reads a codebase into a contract graph and
// checks, slices and draws it. This is the composition root.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

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

const version = "0.2.0"

// launcher starts the live map and the MCP server.
type launcher struct {
	service   *app.Service
	tests     *app.Tests
	watcher   *filesystem.Watcher
	workspace *filesystem.Workspace
}

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

	workspace := filesystem.NewWorkspace(root)
	extractors := app.Extractors{golang.NewExtractor(), javascript.NewExtractor(), python.NewExtractor()}
	service := app.NewService(
		root,
		filesystem.NewConfig(root),
		extractors,
		[]app.Dialect{golang.NewDialect(), javascript.NewDialect(javascript.LanguageJavaScript), javascript.NewDialect(javascript.LanguageTypeScript), python.NewDialect()},
		gitvcs.New(root),
		workspace,
		htmlmap.New(),
		filesystem.NewAgentConfig(root),
		filesystem.NewThemes(treatyHome("themes")),
		filesystem.NewPreferences(treatyHome("settings.json")),
	)

	tests := app.NewTests(root, extractors, []app.TestSuite{golang.NewTestSuite()})
	start := launcher{service: service, tests: tests, watcher: filesystem.NewWatcher(root), workspace: workspace}
	os.Exit(cli.Run(args, service, start, os.Stdin, os.Stdout, os.Stderr))
}

// MCP serves the live graph over stdio. When a live map is already served
// for this repository, it follows that server's baseline and selection
// instead of serving a second map.
func (this launcher) MCP(port int, in io.Reader, out, stderr io.Writer) error {
	stop := make(chan struct{})
	defer close(stop)
	if url, _ := this.workspace.Announced(); url != "" {
		if peer := web.NewPeer(url); peer.Alive() {
			live := app.NewLive(this.service, this.watcher, peer)
			live.Start(stop)
			fmt.Fprintf(stderr, "treaty: a treaty server is already running in this directory; open its live map at %s\n", url)
			return mcp.New(live, version, url, true).Serve(in, out)
		}
	}

	live := app.NewLive(this.service, this.watcher, nil)
	live.UseTests(this.tests)
	live.Start(stop)
	server, withdraw, err := this.listen(live, port)
	if err != nil {
		return err
	}

	defer withdraw()
	defer func() { _ = server.Close() }()
	fmt.Fprintf(stderr, "treaty: open the live map at %s\n", server.URL())
	return mcp.New(live, version, server.URL(), false).Serve(in, out)
}

// URL finds the live map served for this directory.
func (this launcher) URL() (url string, err error) {
	url, err = this.workspace.Announced()
	if err != nil || url == "" || !web.NewPeer(url).Alive() {
		return "", err
	}

	return url, nil
}

// Serve serves the live map until interrupted, unless a treaty server is
// already running in this directory, in which case it points there.
func (this launcher) Serve(port int, open bool, stderr io.Writer) error {
	if url, _ := this.URL(); url != "" {
		fmt.Fprintf(stderr, "treaty: a treaty server is already running in this directory; open its live map at %s\n", url)
		if open {
			browse(url)
		}

		return nil
	}

	stop := make(chan struct{})
	defer close(stop)
	live := app.NewLive(this.service, this.watcher, nil)
	live.UseTests(this.tests)
	live.Start(stop)
	server, withdraw, err := this.listen(live, port)
	if err != nil {
		return err
	}

	defer withdraw()
	defer func() { _ = server.Close() }()
	fmt.Fprintf(stderr, "treaty: live map at %s (Ctrl-C to stop)\n", server.URL())
	if open {
		browse(server.URL())
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	<-interrupted
	return nil
}

func (this launcher) listen(live *app.Live, port int) (*web.Server, func(), error) {
	server, err := web.Listen(live, port)
	if err != nil {
		return nil, nil, err
	}

	withdraw, err := this.workspace.Announce(server.URL())
	if err != nil {
		_ = server.Close()
		return nil, nil, err
	}

	return server, withdraw, nil
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
