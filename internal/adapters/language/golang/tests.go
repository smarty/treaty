package golang

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
)

const (
	// externalSuffix marks the module an external test package, such as
	// graph_test, is placed in while its tests are resolved, so its names
	// never collide with the package it tests.
	externalSuffix = "_test"

	// maxOutput is how much of one test's output is kept.
	maxOutput = 32 * 1024

	// stopGrace is how long go test may take to stop after an interrupt
	// before it is killed.
	stopGrace = 5 * time.Second
)

var ErrGoTest = errors.New("go test failed")

// TestSuite finds Go tests by reading _test.go files and runs them with the
// go command.
type TestSuite struct {
	command string
}

// event is one line of go test -json output.
type event struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Elapsed    float64
	Output     string
}

// invocation is one go test command: packages run whole, or one package's
// tests selected by a -run pattern.
type invocation struct {
	dir      string
	packages []string
	run      string
}

// testDeclaration is one declaration of a test file, placed in the scratch
// graph discovery resolves against.
type testDeclaration struct {
	symbol      *graph.Symbol
	declaration *declaration
	state       *fileState
}

// NewTestSuite creates a Go test suite that runs the go command on the
// PATH.
//
// Returns:
//   - result: the suite.
func NewTestSuite() *TestSuite {
	return &TestSuite{command: "go"}
}

// Discover finds every Test and Fuzz function in the tree's _test.go files
// and the symbols each one uses.
//
// Notes:
//   - A test uses what it names and what the helpers it calls in its own
//     test package name, transitively. Naming a type declared in a test
//     file, such as a fixture, reaches that type's methods too.
//   - A test named for a symbol, such as TestService_Map for Service.Map,
//     targets it even when it never names it.
//
// Parameters:
//   - root: the repository root.
//   - g: the graph of root.
//
// Returns:
//   - result: the tests, without ids.
//   - err: a test file could not be read.
func (this *TestSuite) Discover(root string, g *graph.Graph) (result []app.TestCase, err error) {
	files, roots, err := goFiles(root, true)
	if err != nil {
		return nil, err
	}

	packages := map[string]string{}
	for _, module := range g.Modules {
		if module.Language == "go" {
			packages[module.Path] = module.Name
		}
	}

	scratch := g.Clone()
	var declared []testDeclaration
	for _, relative := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, err
		}

		dir := path.Dir(relative)
		state := &fileState{sourceFile: parseFile(src), path: relative, module: graph.ModuleID("go", dir), imports: map[string]string{}, aliases: map[string]bool{}, lines: strings.Split(string(src), "\n")}
		if strings.HasSuffix(state.pkg, externalSuffix) && state.pkg != packages[dir] {
			state.module += externalSuffix
		}

		state.resolveImports(graph.New(), roots, packages, map[string]bool{})
		for _, each := range state.declarations {
			symbol := scratch.AddSymbol(&graph.Symbol{ID: graph.SymbolID(state.module, each.name), Module: state.module, Name: each.name, Parent: each.parent, Kind: each.kind, File: relative, Line: each.line})
			declared = append(declared, testDeclaration{symbol: symbol, declaration: each, state: state})
		}
	}

	methods := map[string][]*graph.Symbol{}
	for _, symbol := range scratch.Symbols {
		if symbol.Kind == graph.KindMethod {
			_, name, _ := strings.Cut(symbol.Name, ".")
			methods[name] = append(methods[name], symbol)
		}
	}

	helpers := map[string]bool{}
	members := map[string][]string{}
	for _, each := range declared {
		helpers[each.symbol.ID] = true
		if each.symbol.Parent != "" {
			owner := graph.SymbolID(each.symbol.Module, each.symbol.Parent)
			members[owner] = append(members[owner], each.symbol.ID)
		}
	}

	uses := map[string][]string{}
	for _, each := range declared {
		from := each.symbol.ID
		each.state.resolve(scratch, each.symbol.Parent, each.declaration, methods, func(target string, _ token, _ string) {
			if target != from && scratch.Symbol(target) != nil && !slices.Contains(uses[from], target) {
				uses[from] = append(uses[from], target)
			}
		})
	}

	for _, each := range declared {
		if !isTest(each.declaration) {
			continue
		}

		module := strings.TrimSuffix(each.symbol.Module, externalSuffix)
		targets := reach(each.symbol.ID, uses, members, helpers)
		if named := namedTarget(g, module, each.declaration.name); named != "" && !slices.Contains(targets, named) {
			targets = append(targets, named)
		}

		slices.Sort(targets)
		result = append(result, app.TestCase{Module: module, Name: each.declaration.name, File: each.symbol.File, Line: each.symbol.Line, Targets: targets})
	}

	return result, nil
}

// Language is go.
//
// Returns:
//   - result: the id prefix of Go modules.
func (this *TestSuite) Language() string {
	return "go"
}

// Run runs Go tests with go test -json, collecting coverage.
//
// Notes:
//   - Modules run whole share one go test per Go module, so the go command
//     runs their packages in parallel. Named tests run one go test per
//     package, and one per test that names a subtest.
//   - Tests that fail are not an error: they are outcomes.
//
// Parameters:
//   - ctx: cancels the run; go test is interrupted, then killed.
//   - root: the repository root.
//   - requests: the tests to run, by module.
//   - report: receives every outcome.
//
// Returns:
//   - coverage: the lines covered and missed, by file.
//   - err: go test could not run, such as when the go command is missing.
//
// Errors:
//   - ErrGoTest: go test exited without running anything.
func (this *TestSuite) Run(ctx context.Context, root string, requests []app.TestRequest, report func(app.TestOutcome)) (coverage map[string]app.LineCoverage, err error) {
	_, roots, err := goFiles(root, false)
	if err != nil {
		return nil, err
	}

	lines := map[string]map[int]bool{}
	var failures []string
	for _, each := range plan(roots, requests) {
		if ctx.Err() != nil {
			break
		}

		if err := this.invoke(ctx, root, roots, each, report, lines); err != nil {
			failures = append(failures, err.Error())
		}
	}

	coverage = map[string]app.LineCoverage{}
	for file, hits := range lines {
		var each app.LineCoverage
		for line, hit := range hits {
			if hit {
				each.Covered = append(each.Covered, line)
			} else {
				each.Uncovered = append(each.Uncovered, line)
			}
		}

		slices.Sort(each.Covered)
		slices.Sort(each.Uncovered)
		coverage[file] = each
	}

	if len(failures) > 0 {
		return coverage, fmt.Errorf("%w: %s", ErrGoTest, strings.Join(failures, "; "))
	}

	return coverage, nil
}

// invoke runs one go test, reporting its events and adding its coverage
// profile to lines.
func (this *TestSuite) invoke(ctx context.Context, root string, roots moduleRoots, each invocation, report func(app.TestOutcome), lines map[string]map[int]bool) error {
	profile, err := os.CreateTemp("", "treaty-cover-*.out")
	if err != nil {
		return err
	}

	_ = profile.Close()
	defer func() { _ = os.Remove(profile.Name()) }()
	args := []string{"test", "-json", "-coverprofile=" + profile.Name()}
	if each.run != "" {
		args = append(args, "-run", each.run)
	}

	command := exec.CommandContext(ctx, this.command, append(args, each.packages...)...)
	command.Dir = filepath.Join(root, filepath.FromSlash(each.dir))
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = stopGrace
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}

	if err := command.Start(); err != nil {
		return err
	}

	seen := follow(stdout, roots, report)
	waitErr := command.Wait()
	readProfile(profile.Name(), roots, lines)
	if waitErr != nil && !seen && ctx.Err() == nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}

		return errors.New(message)
	}

	return nil
}

// owner finds the Go module holding a directory: the one whose directory is
// the longest prefix of it, else the root.
func (this moduleRoots) owner(dir string) moduleRoot {
	rank := func(dir string) int {
		if dir == "." {
			return 0
		}

		return len(dir)
	}

	best, bestRank := moduleRoot{dir: "."}, -1
	for _, root := range this {
		inside := root.dir == "." || dir == root.dir || strings.HasPrefix(dir, root.dir+"/")
		if inside && rank(root.dir) > bestRank {
			best, bestRank = root, rank(root.dir)
		}
	}

	return best
}

// blockLines reads the first and last lines of a profile block such as
// 12.34,15.2.
func blockLines(span string) (start, end int, ok bool) {
	from, to, found := strings.Cut(span, ",")
	if !found {
		return 0, 0, false
	}

	startText, _, _ := strings.Cut(from, ".")
	endText, _, _ := strings.Cut(to, ".")
	start, err := strconv.Atoi(startText)
	if err != nil {
		return 0, 0, false
	}

	end, err = strconv.Atoi(endText)
	return start, end, err == nil
}

// follow reads go test -json events and reports them as outcomes, keeping
// each test's output for its final outcome.
//
// Returns:
//   - seen: some event named a package.
func follow(stdout io.Reader, roots moduleRoots, report func(app.TestOutcome)) (seen bool) {
	outputs := map[string]*strings.Builder{}
	write := func(key, text string) {
		if outputs[key] == nil {
			outputs[key] = &strings.Builder{}
		}

		if outputs[key].Len() < maxOutput {
			outputs[key].WriteString(text)
		}
	}

	take := func(key string) string {
		if outputs[key] == nil {
			return ""
		}

		text := outputs[key].String()
		delete(outputs, key)
		return text
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var current event
		if json.Unmarshal(scanner.Bytes(), &current) != nil {
			continue
		}

		importPath := current.Package
		if importPath == "" {
			importPath, _, _ = strings.Cut(current.ImportPath, " ")
		}

		dir, ok := roots.locate(importPath)
		if !ok {
			continue
		}

		seen = true
		module := graph.ModuleID("go", dir)
		key := module + "#" + current.Test
		switch current.Action {
		case "build-output", "output":
			write(key, current.Output)
		case "run":
			report(app.TestOutcome{Module: module, Name: current.Test, Status: app.TestRunning})
		case "pass", "fail", "skip":
			report(app.TestOutcome{Module: module, Name: current.Test, Status: current.Action, Elapsed: current.Elapsed, Output: take(key)})
		}
	}

	return seen
}

// isTest reports whether a declaration is a function go test runs by name:
// TestXxx(t *testing.T) or FuzzXxx(f *testing.F).
func isTest(each *declaration) bool {
	if each.kind != graph.KindFunction || each.parent != "" {
		return false
	}

	for prefix, parameter := range map[string]string{"Test": "*testing.T)", "Fuzz": "*testing.F)"} {
		rest, ok := strings.CutPrefix(each.name, prefix)
		if ok && (rest == "" || !strings.ContainsAny(rest[:1], "abcdefghijklmnopqrstuvwxyz")) && strings.HasSuffix(strings.TrimSpace(each.signature), parameter) {
			return true
		}
	}

	return false
}

// moduleDir is the directory a Go module id names.
func moduleDir(module string) string {
	return strings.TrimPrefix(module, "go:")
}

// namedTarget finds the symbol of a module a test is named for: TestParse
// for Parse, and TestService_Map or TestServiceMap for Service.Map.
func namedTarget(g *graph.Graph, module, name string) string {
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok {
		rest = strings.TrimPrefix(name, "Fuzz")
	}

	if rest == "" {
		return ""
	}

	for _, symbol := range g.SymbolsIn(module) {
		dotted := strings.ReplaceAll(symbol.Name, ".", "_")
		if symbol.Name == rest || dotted == rest || strings.ReplaceAll(symbol.Name, ".", "") == rest {
			return symbol.ID
		}
	}

	return ""
}

// packageArgument names a package directory for go test run in its Go
// module's directory, such as ./internal/app, or . for that directory.
func packageArgument(moduleDir, dir string) string {
	relative := dir
	if moduleDir != "." {
		relative = strings.TrimPrefix(strings.TrimPrefix(dir, moduleDir), "/")
	}

	if relative == "" || relative == "." {
		return "."
	}

	return "./" + relative
}

// plan turns requests into go test invocations, each run where its Go
// module's go.mod is.
func plan(roots moduleRoots, requests []app.TestRequest) (result []invocation) {
	whole := map[string]int{}
	for _, request := range requests {
		dir := moduleDir(request.Module)
		owner := roots.owner(dir)
		target := packageArgument(owner.dir, dir)

		if len(request.Names) == 0 {
			if at, ok := whole[owner.dir]; ok {
				result[at].packages = append(result[at].packages, target)
				continue
			}

			whole[owner.dir] = len(result)
			result = append(result, invocation{dir: owner.dir, packages: []string{target}})
			continue
		}

		var tops []string
		for _, name := range request.Names {
			if strings.Contains(name, "/") {
				result = append(result, invocation{dir: owner.dir, packages: []string{target}, run: runPattern(strings.Split(name, "/"))})
				continue
			}

			tops = append(tops, regexp.QuoteMeta(name))
		}

		if len(tops) > 0 {
			result = append(result, invocation{dir: owner.dir, packages: []string{target}, run: "^(" + strings.Join(tops, "|") + ")$"})
		}
	}

	return result
}

// reach collects the symbols outside test files a test uses, following
// helpers in test files and, from a test file's type, its methods.
func reach(start string, uses, members map[string][]string, helpers map[string]bool) (result []string) {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		next := append(slices.Clone(uses[current]), members[current]...)
		for _, target := range next {
			if seen[target] {
				continue
			}

			seen[target] = true
			if helpers[target] {
				queue = append(queue, target)
			} else {
				result = append(result, target)
			}
		}
	}

	return result
}

// readProfile adds a coverage profile's blocks to lines: a line is covered
// when some block on it ran, and missed when every block on it did not.
func readProfile(name string, roots moduleRoots, lines map[string]map[int]bool) {
	data, err := os.ReadFile(name)
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		location, counts, ok := strings.Cut(line, " ")
		if !ok || strings.HasPrefix(line, "mode:") {
			continue
		}

		at := strings.LastIndex(location, ":")
		fields := strings.Fields(counts)
		if at < 0 || len(fields) != 2 {
			continue
		}

		dir, found := roots.locate(path.Dir(location[:at]))
		if !found {
			continue
		}

		start, end, ok := blockLines(location[at+1:])
		count, err := strconv.Atoi(fields[1])
		if !ok || err != nil {
			continue
		}

		file := path.Join(dir, path.Base(location[:at]))
		if lines[file] == nil {
			lines[file] = map[int]bool{}
		}

		for each := start; each <= end; each++ {
			lines[file][each] = lines[file][each] || count > 0
		}
	}
}

// runPattern selects one subtest: each level of the name matched exactly.
func runPattern(levels []string) string {
	for index, level := range levels {
		levels[index] = "^" + regexp.QuoteMeta(level) + "$"
	}

	return strings.Join(levels, "/")
}
