// Package cli is the command-line driving adapter for treaty.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"

	"github.com/smarty/treaty/internal/app"
)

const usage = `usage: treaty <command> [flags]

commands:
  check [--base <ref>] [--format json|text]   run every check; exit 1 on failure
  dump [--at <ref>]                           print the graph in CML
  slice <symbol or module>                    print a context slice as JSON
  design new <name> [--from <target>]...      scaffold a design in .treaty/designs
  design check <name> [--format json|text]    compare a design with the code
  map [--base <ref>] [--design <name>]...     render a static map to .treaty/out/map.html
  serve [--port <n>] [--open]                 serve the live map until interrupted
  mcp [--port <n>]                            serve the live graph to an agent over MCP on
                                              stdio, and the live map to the person
  init [--architecture <name>]                create .treaty and propose treaty.yaml for an
                                              architecture: hexagonal (default), clean,
                                              layered, slices or modular
  url                                         print the live map's address for this directory
  here [--force]                              register treaty in this repository's .mcp.json,
                                              so Claude Code sessions here start it
`

var ErrUsage = errors.New("usage")

// Launcher starts the long-running servers; the composition root wires
// them, since they need adapters the CLI may not import.
type Launcher interface {
	// MCP serves the live graph over MCP on in and out until in closes,
	// and serves the live map unless another server already does.
	MCP(port int, in io.Reader, out, stderr io.Writer) error

	// Serve serves the live map until interrupted.
	Serve(port int, open bool, stderr io.Writer) error

	// URL finds the live map served for this directory, empty when no
	// treaty server is running here.
	URL() (url string, err error)
}

// repeated collects a flag given more than once.
type repeated []string

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
	port := flags.Int("port", 7878, "port for the live map; any free port if it is taken")
	open := flags.Bool("open", false, "open the live map in a browser")
	force := flags.Bool("force", false, "replace a different treaty entry in .mcp.json")
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

		if err := write(stdout, *format, report, func() string { return checkText(report) }); err != nil {
			return err
		}

		if len(report.Failures) > 0 {
			return failure{strings.Join(report.Failures, "; ")}
		}

		return nil
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

		return write(stdout, "json", slice, nil)
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
		url, err := launcher.URL()
		if err != nil {
			return err
		}

		if url == "" {
			message := "no treaty server is running in this directory; start one with treaty serve, or open Claude Code here after treaty here"
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
	default:
		return ErrUsage
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
