// Package cli is the command-line driving adapter for treaty.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/smarty/treaty/internal/app"
)

const usage = `usage: treaty [-C <dir>] <command> [flags]

  -C, --dir <dir>                             work in the repository at dir instead of the
                                              current directory

commands:
  check [--base <ref>] [--format json|text|summary]
                                              run every check; exit 1 on failure
  overview [--base <ref>]                     print every module by layer and every module
                                              dependency, in a few kilobytes
  find <query> [--kind <kind>]                list symbols and struct fields whose id contains
                                              query, with file:line and signature
  slice <target> [--format text|json]         print a context slice; targets everywhere may be
                                              ids, files or short names such as Store.Open
  source <symbol or file[:start-end]> [--all]
                                              print just that code, with line numbers; a long
                                              file prints its outline unless --all
  impact <symbol, module or file>             list everything that depends on the target
  allowed <from module> <to module>           say whether from may depend on to
  dump [--at <ref>]                           print the graph in AutoPen
  design new <name> [--from <target>]...      scaffold a design in .treaty/designs
  design check <name> [--format json|text]    compare a design with the code
  map [--base <ref>] [--design <name>]...     render a static map to .treaty/out/map.html
  serve [--port <n>] [--open]                 keep this directory's live map open until
                                              interrupted
  mcp [--port <n>]                            serve the live graph to an agent over MCP on
                                              stdio, and the live map to the person
  restart [--port <n>]                        stop the treaty server; open sessions start
                                              a new one, such as a newly built treaty
  init [--architecture <name>]                create .treaty and propose treaty.yaml for an
                                              architecture: none (default), hexagonal,
                                              clean, layered, slices or modular
  url [--port <n>]                            print the live map's address for this directory
  here [--force]                              register treaty in this repository's .mcp.json,
                                              so Claude Code sessions here start it
`

var ErrUsage = errors.New("usage")

// Launcher reaches the one treaty server on this machine, which serves
// every open project's live map and every agent session; the composition
// root wires it, since it needs adapters the CLI may not import.
type Launcher interface {
	// Daemon is the treaty server itself, which runs until no session has
	// been open for a while, it is asked to stop, or treaty is rebuilt.
	Daemon(port int, stderr io.Writer) error

	// MCP relays the live graph over MCP on in and out until in closes,
	// starting the treaty server when none runs.
	MCP(port int, in io.Reader, out, stderr io.Writer) error

	// Restart stops the treaty server, so its sessions start a new one.
	Restart(port int, stderr io.Writer) error

	// Serve keeps this directory's live map open until interrupted.
	Serve(port int, open bool, stderr io.Writer) error

	// URL finds the live map of this directory, empty when it is not open.
	URL(port int) (url string, err error)
}

// repeated collects a flag given more than once.
type repeated []string

// Directory reads the -C <dir> or --dir <dir> option that may lead the
// command line, naming the repository to work in.
//
// Parameters:
//   - args: the command line without the program name.
//   - workingDir: the current directory, used when no option is given and to
//     resolve a relative dir.
//
// Returns:
//   - dir: the absolute repository directory.
//   - rest: the command line after the option.
//   - err: the option has no value, or dir is not a directory.
//
// Errors:
//   - ErrUsage: -C or --dir is not followed by a directory.
func Directory(args []string, workingDir string) (dir string, rest []string, err error) {
	dir, rest = workingDir, args
	if len(args) > 0 {
		switch {
		case args[0] == "-C" || args[0] == "--dir" || args[0] == "-dir":
			if len(args) < 2 {
				return "", nil, ErrUsage
			}

			dir, rest = args[1], args[2:]
		case strings.HasPrefix(args[0], "--dir="):
			dir, rest = strings.TrimPrefix(args[0], "--dir="), args[1:]
		}
	}

	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workingDir, dir)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", nil, err
	}

	if !info.IsDir() {
		return "", nil, fmt.Errorf("%s is not a directory", dir)
	}

	return filepath.Clean(dir), rest, nil
}

// Run executes one treaty command.
//
// Parameters:
//   - args: the command line without the program name.
//   - service: the use cases.
//   - launcher: starts the live servers, for the serve and mcp commands.
//   - stdin: input for the mcp command.
//   - stdout: where results go.
//   - stderr: where errors and summaries go.
//
// Returns:
//   - code: the process exit code: 0 on success, 1 when a check fails, 2 on
//     errors.
func Run(args []string, service *app.Service, launcher Launcher, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	err := run(args, service, launcher, stdin, stdout, stderr)
	var failed failure
	switch {
	case err == nil:
		return 0
	case errors.As(err, &failed):
		return 1
	case errors.Is(err, ErrUsage):
		fmt.Fprint(stderr, usage)
		return 2
	default:
		fmt.Fprintf(stderr, "treaty: %v\n", err)
		return 2
	}
}

// failure marks a check that ran and failed.
type failure struct{ message string }

func (this failure) Error() string { return this.message }

func (this *repeated) Set(value string) error {
	*this = append(*this, value)
	return nil
}

func (this *repeated) String() string { return strings.Join(*this, ",") }

func run(args []string, service *app.Service, launcher Launcher, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}

	command, args := args[0], args[1:]
	if command == "design" {
		if len(args) == 0 {
			return ErrUsage
		}

		command, args = "design "+args[0], args[1:]
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	architecture := flags.String("architecture", "", "architecture for init: clean, hexagonal, layered, modular or slices")
	base := flags.String("base", "", "git ref to diff against")
	at := flags.String("at", "", "git ref to read instead of the working tree")
	format := flags.String("format", "json", "json or text")
	port := flags.Int("port", 7878, "port of the treaty server, the same for every session")
	open := flags.Bool("open", false, "open the live map in a browser")
	force := flags.Bool("force", false, "replace a different treaty entry in .mcp.json")
	all := flags.Bool("all", false, "print a whole file or long range from source, not its outline")
	kind := flags.String("kind", "", "symbol kind for find: function, method, interface, type, value or field")
	var from, designs repeated
	flags.Var(&from, "from", "module or symbol to copy into the design")
	flags.Var(&designs, "design", "design to overlay")
	positional, err := parseInterleaved(flags, args)
	if err != nil {
		return ErrUsage
	}

	switch command {
	case "check":
		report, err := service.Check(*base)
		if err != nil {
			return err
		}

		render := func() string { return checkText(report) }
		if *format == "summary" {
			*format, render = "text", report.Summary
		}

		if err := write(stdout, *format, report, render); err != nil {
			return err
		}

		if len(report.Failures) > 0 {
			return failure{strings.Join(report.Failures, "; ")}
		}

		return nil
	case "overview":
		if len(positional) != 0 {
			return ErrUsage
		}

		return text(stdout)(service.Overview(*base))
	case "find":
		if len(positional) != 1 {
			return ErrUsage
		}

		return text(stdout)(service.Find(positional[0], *kind))
	case "impact":
		if len(positional) != 1 {
			return ErrUsage
		}

		return text(stdout)(service.Impact(positional[0]))
	case "allowed":
		if len(positional) != 2 {
			return ErrUsage
		}

		return text(stdout)(service.Allowed(positional[0], positional[1]))
	case "dump":
		text, err := service.Dump(*at)
		if err != nil {
			return err
		}

		_, err = io.WriteString(stdout, text)
		return err
	case "slice":
		if len(positional) != 1 {
			return ErrUsage
		}

		slice, err := service.Slice(positional[0])
		if err != nil {
			return err
		}

		sliceFormat := "text"
		flags.Visit(func(given *flag.Flag) {
			if given.Name == "format" {
				sliceFormat = *format
			}
		})

		return write(stdout, sliceFormat, slice, slice.Text)
	case "source":
		if len(positional) != 1 {
			return ErrUsage
		}

		return text(stdout)(service.Source(positional[0], *all))
	case "design new":
		if len(positional) != 1 {
			return ErrUsage
		}

		path, err := service.DesignNew(positional[0], from)
		if err != nil {
			return err
		}

		fmt.Fprintln(stdout, path)
		return nil
	case "design check":
		if len(positional) != 1 {
			return ErrUsage
		}

		report, err := service.DesignCheck(positional[0])
		if err != nil {
			return err
		}

		if err := write(stdout, *format, report, func() string { return designText(report) }); err != nil {
			return err
		}

		if report.Failures > 0 {
			return failure{fmt.Sprintf("%d design item(s) fail", report.Failures)}
		}

		return nil
	case "map":
		path, err := service.Map(*base, designs)
		if err != nil {
			return err
		}

		fmt.Fprintln(stdout, path)
		return nil
	case "init":
		path, err := service.Init(*architecture)
		if err != nil {
			return err
		}

		fmt.Fprintf(stdout, "created .treaty/ and wrote %s\n", path)
		return nil
	case "url":
		url, err := launcher.URL(*port)
		if err != nil {
			return err
		}

		if url == "" {
			message := "this directory's live map is not open; open it with treaty serve, or open Claude Code here after treaty here"
			fmt.Fprintln(stderr, message)
			return failure{message}
		}

		fmt.Fprintln(stdout, url)
		return nil
	case "here":
		report, err := service.Here(*force)
		if err != nil {
			return err
		}

		fmt.Fprint(stdout, hereText(report))
		return nil
	case "serve":
		return launcher.Serve(*port, *open, stderr)
	case "mcp":
		return launcher.MCP(*port, stdin, stdout, stderr)
	case "restart":
		return launcher.Restart(*port, stderr)
	case "daemon":
		return launcher.Daemon(*port, stderr)
	default:
		return ErrUsage
	}
}

// text writes a command's text result, or returns its error.
func text(stdout io.Writer) func(result string, err error) error {
	return func(result string, err error) error {
		if err != nil {
			return err
		}

		_, err = io.WriteString(stdout, result)
		return err
	}
}

func checkText(report app.Report) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Modules (%d)\n", len(report.Modules))
	for _, module := range report.Modules {
		layer := module.Layer
		if module.Side != "" {
			layer += "/" + module.Side
		}

		if module.Section != "" {
			layer = path.Base(module.Section) + "/" + layer
		}

		if module.Public {
			layer += " (public)"
		}

		m := module.Metrics
		fmt.Fprintf(&builder, "  %-44s %-20s Ca=%d Ce=%d I=%.2f A=%.2f D=%.2f", module.ID, layer, m.Afferent, m.Efferent, m.Instability, m.Abstractness, m.Distance)
		if module.Before != nil && module.Before.Instability != m.Instability {
			fmt.Fprintf(&builder, " (I was %.2f)", module.Before.Instability)
		}

		builder.WriteString("\n")
	}

	for _, note := range report.Notes {
		fmt.Fprintf(&builder, "\nNote: %s\n", note)
	}

	fmt.Fprintf(&builder, "\nReview queue (%d)\n", len(report.Findings))
	for _, finding := range report.Findings {
		fmt.Fprintf(&builder, "  [%s] %s\n         %s\n", strings.ToUpper(finding.Severity[:1]), finding.Title, finding.Detail)
	}

	builder.WriteString("\n")
	if len(report.Failures) > 0 {
		fmt.Fprintf(&builder, "FAIL: %s\n", strings.Join(report.Failures, "; "))
	} else {
		builder.WriteString("PASS\n")
	}

	return builder.String()
}

func designText(report app.DesignReport) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Design %q (%s)\n", report.Title, report.Name)
	for _, item := range report.Items {
		fmt.Fprintf(&builder, "  line %-4d %-12s %s\n            %s\n", item.Line, item.Kind, item.Target, item.Detail)
	}

	if report.Failures > 0 {
		fmt.Fprintf(&builder, "FAIL: %d item(s)\n", report.Failures)
	} else {
		builder.WriteString("PASS\n")
	}

	return builder.String()
}

func hereText(report app.HereReport) string {
	var builder strings.Builder
	path := report.Registration.Path
	switch report.Registration.Outcome {
	case app.RegistrationCreated:
		fmt.Fprintf(&builder, "created %s with the treaty MCP server\n", path)
	case app.RegistrationAdded:
		fmt.Fprintf(&builder, "added the treaty MCP server to %s\n", path)
	case app.RegistrationReplaced:
		fmt.Fprintf(&builder, "replaced the treaty MCP server in %s\n", path)
	default:
		fmt.Fprintf(&builder, "treaty is already registered in %s\n", path)
	}

	builder.WriteString("\nNext:\n")
	if _, err := exec.LookPath("treaty"); err != nil {
		builder.WriteString("  - treaty is not on your PATH, so Claude Code cannot start it: run\n      go install github.com/smarty/treaty/cmd/treaty@latest\n    and make sure $(go env GOPATH)/bin is on your PATH\n")
	}

	if !report.Configured {
		builder.WriteString("  - run treaty init to propose the layers for this repository\n")
	}

	builder.WriteString("  - start Claude Code here and approve the treaty server when asked\n")
	builder.WriteString("  - open the live map at http://127.0.0.1:7878, or ask Claude for its address if that port was taken\n")
	builder.WriteString("  - commit .mcp.json to give everyone who works here the same setup\n")
	return builder.String()
}

func parseInterleaved(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}

		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}

		positional = append(positional, args[0])
		args = args[1:]
	}
}

func write(out io.Writer, format string, value any, text func() string) error {
	if format == "text" && text != nil {
		_, err := io.WriteString(out, text())
		return err
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
