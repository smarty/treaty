package golang

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

// Extractor builds contract graphs from Go source trees.
type Extractor struct{}

// source is one declaration of a symbol and the file it came from. A symbol
// with build variants has several.
type source struct {
	declaration *declaration
	state       *fileState
}

// fileState is one parsed file placed in its module.
type fileState struct {
	*sourceFile
	path       string
	module     string
	imports    map[string]string
	aliases    map[string]bool
	dotImports []string
	lines      []string
}

// moduleRoot is one Go module in the tree: the path its go.mod declares and
// the directory holding that go.mod.
type moduleRoot struct {
	path string
	dir  string
}

// moduleRoots are the tree's Go modules, longest module path first, so the
// most specific module claims an import.
type moduleRoots []moduleRoot

// NewExtractor creates a Go extractor.
//
// Returns:
//   - result: the extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract reads every non-test Go file under root into one graph. A module
// is a directory.
//
// Notes:
//   - A sub-tree holding its own go.mod is read too, as part of the
//     repository: its imports resolve through its own module path, and
//     imports between it and the rest of the repository are dependencies
//     like any other.
//
// Parameters:
//   - root: the directory to read.
//
// Returns:
//   - result: the normalized graph, without layers.
//   - err: a file could not be read.
func (this *Extractor) Extract(root string) (result *graph.Graph, err error) {
	files, roots, err := goFiles(root, false)
	if err != nil {
		return nil, err
	}

	result = graph.New()
	var states []*fileState
	packages := map[string]string{}
	for _, relative := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, err
		}

		dir := path.Dir(relative)
		state := &fileState{sourceFile: parseFile(src), path: relative, module: graph.ModuleID("go", dir), imports: map[string]string{}, aliases: map[string]bool{}, lines: strings.Split(string(src), "\n")}
		states = append(states, state)
		if packages[dir] == "" {
			packages[dir] = state.pkg
		}
	}

	moduleImports := map[string]map[string]bool{}
	var order []*graph.Symbol
	declared := map[*graph.Symbol][]source{}
	for _, state := range states {
		dir := path.Dir(state.path)
		module := result.AddModule(&graph.Module{ID: state.module, Language: "go", Path: dir, Name: packages[dir], Entry: packages[dir] == "main", Private: internal(dir), Manifest: roots.manifest(dir)})
		module.Files = append(module.Files, state.path)
		if moduleImports[state.module] == nil {
			moduleImports[state.module] = map[string]bool{}
		}

		state.resolveImports(result, roots, packages, moduleImports[state.module])
		for _, declaration := range state.declarations {
			candidate := &graph.Symbol{
				ID:        graph.SymbolID(state.module, declaration.name),
				Module:    state.module,
				Name:      declaration.name,
				Parent:    declaration.parent,
				Kind:      declaration.kind,
				File:      state.path,
				Line:      declaration.line,
				EndLine:   declaration.endLine,
				Contract:  contract(declaration),
				Signature: declaration.signature,
				Pointer:   declaration.pointer,
				Fields:    declaration.fields,
				Hash:      hash(declaration.hashText),
			}

			candidate.Doc, candidate.DocLine = docComment(state.lines, declaration.line)

			symbol := result.AddSymbol(candidate)
			if symbol == candidate {
				order = append(order, symbol)
			} else {
				// The same name declared twice in one package is a build
				// variant: each file compiles under different constraints.
				// Repeated init functions and blank declarations are not build
				// variants, so they carry no signature to compare.
				variant := graph.Variant{File: state.path, Line: declaration.line, Signature: declaration.signature, Fields: declaration.fields}
				if repeatable(declaration) {
					variant.Signature, variant.Fields = "", nil
				}

				symbol.Variants = append(symbol.Variants, variant)
				symbol.Hash = hash(symbol.Hash + " " + candidate.Hash)
			}

			declared[symbol] = append(declared[symbol], source{declaration, state})
		}
	}

	methods := map[string][]*graph.Symbol{}
	for _, symbol := range order {
		if symbol.Kind == graph.KindMethod {
			_, name, _ := strings.Cut(symbol.Name, ".")
			methods[name] = append(methods[name], symbol)
		}
	}

	for _, symbol := range order {
		for _, each := range declared[symbol] {
			each.state.references(result, symbol, each.declaration, methods)
		}
	}

	implementsEdges(result, moduleImports)
	result.Normalize()
	return result, nil
}

// Source reads one file under root.
//
// Parameters:
//   - root: the repository root.
//   - file: the path relative to root.
//
// Returns:
//   - result: the file's bytes.
//   - err: the file could not be read.
func (this *Extractor) Source(root, file string) (result []byte, err error) {
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
}

// references records an edge for every identifier in a declaration that
// names another symbol in the graph.
//
// Notes:
//   - pkg.Name resolves through the file's imports; recv.Name through the
//     receiver's type.
//   - x.Method(...) with an unknown x resolves only when exactly one method of
//     that name exists in this module or the modules it imports.
func (this *fileState) references(g *graph.Graph, symbol *graph.Symbol, declaration *declaration, methods map[string][]*graph.Symbol) {
	this.resolve(g, symbol.Parent, declaration, methods, func(target string, at token, kind string) {
		other := g.Symbol(target)
		if other == nil || other.ID == symbol.ID || (symbol.Parent != "" && other.Name == symbol.Parent) {
			return
		}

		if kind == "" {
			kind = graph.EdgeTypeUse
			if other.Kind == graph.KindFunction || other.Kind == graph.KindMethod {
				kind = graph.EdgeCall
			}
		}

		g.AddEdge(graph.Edge{From: symbol.ID, To: target, Kind: kind, File: this.path, Line: at.line})
	})
}

// resolve finds every identifier in a declaration that may name another
// symbol in the graph and hands its id to edge, with the reference's kind
// when the syntax decides it. The id may name nothing; edge checks.
//
// Notes:
//   - parent is the type a method belongs to, so recv.Name resolves.
func (this *fileState) resolve(g *graph.Graph, parent string, declaration *declaration, methods map[string][]*graph.Symbol, edge func(target string, at token, kind string)) {
	for _, embedded := range declaration.embedded {
		if target := this.typeTarget(g, embedded); target != "" {
			edge(target, embedded[0], graph.EdgeEmbeds)
		}
	}

	tokens := declaration.refs
	inCase := false
	for index := 0; index < len(tokens); index++ {
		current := tokens[index]
		switch {
		case current.is("case"):
			inCase = true
		case current.is(":") || current.kind == tokenSemicolon:
			inCase = false
		}

		if current.kind != tokenIdent || isKeyword(current.text) {
			continue
		}

		afterDot := index > 0 && tokens[index-1].is(".")
		selects := index+2 < len(tokens) && tokens[index+1].is(".") && tokens[index+2].kind == tokenIdent
		switch {
		case afterDot:
			if index+1 < len(tokens) && tokens[index+1].is("(") {
				if target := this.uniqueMethod(methods[current.text]); target != "" {
					edge(target, current, graph.EdgeCall)
				}
			}
		case selects && this.imports[current.text] != "":
			edge(graph.SymbolID(this.imports[current.text], tokens[index+2].text), current, "")
			index += 2
		case selects && this.aliases[current.text]:
			index += 2
		case selects && declaration.receiver != "" && current.text == declaration.receiver && g.Symbol(graph.SymbolID(this.module, parent+"."+tokens[index+2].text)) != nil:
			edge(graph.SymbolID(this.module, parent+"."+tokens[index+2].text), current, "")
			index += 2
		case !inCase && index+1 < len(tokens) && tokens[index+1].is(":"):
		default:
			edge(this.local(g, current.text), current, "")
		}
	}
}

// resolveImports maps each import's name to its module and records every
// in-repo import in the graph, blank and dot imports included, since each
// is a dependency whether or not anything it provides is referenced.
func (this *fileState) resolveImports(g *graph.Graph, roots moduleRoots, packages map[string]string, moduleImports map[string]bool) {
	for _, spec := range this.sourceFile.imports {
		alias := spec.alias
		dir, inRepo := roots.locate(spec.path)
		if alias == "" {
			alias = path.Base(spec.path)
			if inRepo && packages[dir] != "" {
				alias = packages[dir]
			}
		}

		target := graph.ModuleID("go", dir)
		if inRepo {
			g.AddImport(graph.Import{From: this.module, To: target, File: this.path, Line: spec.line})
		}

		switch {
		case alias == "_":
		case alias == ".":
			if inRepo {
				this.dotImports = append(this.dotImports, target)
				moduleImports[target] = true
			}
		default:
			this.aliases[alias] = true
			if inRepo {
				this.imports[alias] = target
				moduleImports[target] = true
			}
		}
	}
}

// local resolves an unqualified name: to this module's symbol when it
// declares one, else to the first dot-imported module that does.
func (this *fileState) local(g *graph.Graph, name string) string {
	result := graph.SymbolID(this.module, name)
	if g.Symbol(result) != nil {
		return result
	}

	for _, module := range this.dotImports {
		if candidate := graph.SymbolID(module, name); g.Symbol(candidate) != nil {
			return candidate
		}
	}

	return result
}

func (this *fileState) typeTarget(g *graph.Graph, tokens []token) string {
	if len(tokens) > 0 && tokens[0].is("*") {
		tokens = tokens[1:]
	}

	switch {
	case len(tokens) >= 3 && tokens[1].is("."):
		if module := this.imports[tokens[0].text]; module != "" {
			return graph.SymbolID(module, tokens[2].text)
		}
	case len(tokens) >= 1:
		return this.local(g, tokens[0].text)
	}

	return ""
}

func (this *fileState) uniqueMethod(candidates []*graph.Symbol) string {
	reachable := map[string]bool{this.module: true}
	for _, module := range this.imports {
		reachable[module] = true
	}

	var found []string
	for _, candidate := range candidates {
		if reachable[candidate.Module] {
			found = append(found, candidate.ID)
		}
	}

	if len(found) != 1 {
		return ""
	}

	return found[0]
}

// locate finds the directory of an imported package that lives in this
// tree, through the module whose path is the longest prefix of the import.
func (this moduleRoots) locate(importPath string) (dir string, found bool) {
	for _, root := range this {
		rest, ok := strings.CutPrefix(importPath, root.path)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			continue
		}

		return path.Join(root.dir, strings.TrimPrefix(rest, "/")), true
	}

	return "", false
}

// manifest names the go.mod in a package's directory, or empty when it has
// none.
func (this moduleRoots) manifest(dir string) string {
	for _, root := range this {
		if root.dir == dir {
			return path.Join(dir, "go.mod")
		}
	}

	return ""
}

// internal reports whether Go's internal rule limits a package to importers
// inside this repository: some element of its path is internal.
func internal(dir string) bool {
	return slices.Contains(strings.Split(dir, "/"), "internal")
}

// repeatable reports whether Go allows the declaration's name more than once
// in a package: init functions and the blank identifier.
func repeatable(declaration *declaration) bool {
	return declaration.parent == "" && (declaration.name == "init" || declaration.name == "_")
}

// docComment reads the comment that ends on the line just above a
// declaration, as Go documents declarations, without its comment markers.
// Directives such as //go:build are not documentation.
//
// Parameters:
//   - lines: the file's lines.
//   - line: the declaration's first line, counting from 1.
//
// Returns:
//   - text: the documentation text, empty when there is none.
//   - start: the line the comment starts on, or 0 when there is none.
func docComment(lines []string, line int) (text string, start int) {
	end := line - 2
	if end < 0 || end >= len(lines) {
		return "", 0
	}

	var result []string
	last := strings.TrimSpace(lines[end])
	switch {
	case strings.HasPrefix(last, "//"):
		for index := end; index >= 0; index-- {
			text := strings.TrimSpace(lines[index])
			if !strings.HasPrefix(text, "//") {
				break
			}

			start = index + 1
			if directive(text) {
				continue
			}

			text = strings.TrimPrefix(text, "//")
			result = append([]string{strings.TrimPrefix(text, " ")}, result...)
		}
	case strings.HasSuffix(last, "*/"):
		for index := end; index >= 0; index-- {
			text := strings.TrimSpace(lines[index])
			opening := strings.Index(text, "/*")
			if opening >= 0 {
				text = text[opening+2:]
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text, "*/"), "*"))
			result = append([]string{text}, result...)
			if opening >= 0 {
				start = index + 1
				break
			}
		}
	}

	text = strings.TrimSpace(strings.Join(result, "\n"))
	if text == "" {
		return "", 0
	}

	return text, start
}

// directive reports whether a line comment is a tool directive, such as
// //go:build or //line, which Go leaves out of documentation.
func directive(text string) bool {
	return strings.HasPrefix(text, "//go:") || strings.HasPrefix(text, "//line ") || strings.HasPrefix(text, "//nolint")
}

func contract(declaration *declaration) bool {
	_, name, found := strings.Cut(declaration.name, ".")
	if !found {
		return exported(declaration.name)
	}

	return exported(name) && exported(declaration.parent)
}

// goFiles lists every non-test Go file under root, or with tests every test
// file, and the Go modules that hold them: the root's go.mod and every go.mod
// in a sub-tree.
func goFiles(root string, tests bool) (files []string, roots moduleRoots, err error) {
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		name := entry.Name()
		relative, _ := filepath.Rel(root, current)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if current != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}

			if modulePath := readGoModule(filepath.Join(current, "go.mod")); modulePath != "" {
				roots = append(roots, moduleRoot{path: modulePath, dir: relative})
			}

			return nil
		}

		if strings.HasSuffix(name, ".go") && strings.HasSuffix(name, "_test.go") == tests {
			files = append(files, relative)
		}

		return nil
	})

	sort.Strings(files)
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].path) > len(roots[j].path) })
	return files, roots, err
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(collapse(text)))
	return hex.EncodeToString(sum[:6])
}

// implementsEdges links each concrete type to every interface whose method
// set it covers, in its own module or a module it imports. Go interfaces are
// implicit, so this is a structural match on method keys.
func implementsEdges(g *graph.Graph, moduleImports map[string]map[string]bool) {
	methods := map[string]map[string]bool{}
	for _, symbol := range g.Symbols {
		if symbol.Kind != graph.KindMethod {
			continue
		}

		owner := graph.SymbolID(symbol.Module, symbol.Parent)
		if methods[owner] == nil {
			methods[owner] = map[string]bool{}
		}

		methods[owner][signatureKey(symbol.Signature)] = true
	}

	for _, concrete := range g.Symbols {
		if concrete.Kind != graph.KindType || len(methods[concrete.ID]) == 0 {
			continue
		}

		for _, iface := range g.Symbols {
			if iface.Kind != graph.KindInterface || len(methods[iface.ID]) == 0 {
				continue
			}

			if iface.Module != concrete.Module && !moduleImports[concrete.Module][iface.Module] {
				continue
			}

			satisfied := true
			for key := range methods[iface.ID] {
				if !methods[concrete.ID][key] {
					satisfied = false
					break
				}
			}

			if satisfied {
				g.AddEdge(graph.Edge{From: concrete.ID, To: iface.ID, Kind: graph.EdgeImplements, File: concrete.File, Line: concrete.Line})
			}
		}
	}
}

// readGoModule reads the module path a go.mod declares, or empty when the
// file is missing or declares none.
func readGoModule(name string) string {
	file, err := os.Open(name)
	if err != nil {
		return ""
	}

	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, "module ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
		}
	}

	return ""
}

// signatureKey parses a AutoPen function line and returns its comparison key,
// or the line itself when it does not parse.
func signatureKey(signature string) string {
	src := []byte(signature)
	tokens := lex(src)
	if len(tokens) == 0 || !tokens[0].is("func") {
		return signature
	}

	parsed, _ := readFuncSignature(tokens, 1)
	if parsed.name == "" {
		return signature
	}

	return parsed.key(src)
}
