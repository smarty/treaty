package python

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

const Language = "py"

var manifestNames = []string{"pyproject.toml", "setup.py", "setup.cfg"}

// Extractor builds contract graphs from Python source trees.
type Extractor struct{}

// binding is a local name an import binds: to another file's name, or to a
// module or package itself when name is empty.
type binding struct {
	at   location
	name string
}

// fileState is one parsed file placed in its module.
type fileState struct {
	*sourceFile
	path      string
	module    string
	lines     []string
	symbols   map[string]string
	bindings  map[string]binding
	stars     []*fileState
	reachable map[string]bool
}

// location is a module or package that an import can name: a .py file, a
// package's __init__.py, or a namespace package with no file.
type location struct {
	path string
	file *fileState
}

// resolver places dotted module names in the tree, from the source roots:
// the repository root, src, and each project's directory and its src.
type resolver struct {
	byPath   map[string]*fileState
	dirs     map[string]bool
	packages map[string][]string
	roots    []string
}

// source is one declaration of a symbol and the file it came from.
type source struct {
	declaration *declaration
	state       *fileState
}

// tree is everything the walk finds: source files and the project manifest
// of each directory that has one.
type tree struct {
	files     []string
	manifests map[string]string
}

// NewExtractor creates a Python extractor.
//
// Returns:
//   - result: the extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract reads every non-test Python file under root into one graph. A
// module is a directory, as a package is.
//
// Notes:
//   - Python scopes names to a file, but a module is a directory. A
//     top-level name defined in more than one file of a directory is
//     qualified by its file's name, as in client/connect.
//   - Names starting with an underscore are not contracts; special names
//     such as __init__ are.
//   - Tests (test_*.py, *_test.py, conftest.py, tests directories), stubs
//     (.pyi), virtual environments, and build and dist directories are
//     skipped.
//   - A package is an entry point only when every file in it is a script.
//
// Parameters:
//   - root: the directory to read.
//
// Returns:
//   - result: the normalized graph, without layers.
//   - err: a file could not be read.
func (this *Extractor) Extract(root string) (result *graph.Graph, err error) {
	found, err := walk(root)
	if err != nil {
		return nil, err
	}

	result = graph.New()
	var states []*fileState
	imports := &resolver{byPath: map[string]*fileState{}, dirs: map[string]bool{}}
	declaredIn := map[string]map[string]map[string]bool{}
	for _, file := range found.files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, err
		}

		dir := path.Dir(file)
		state := &fileState{
			sourceFile: parseFile(src),
			path:       file,
			module:     graph.ModuleID(Language, dir),
			lines:      strings.Split(string(src), "\n"),
			symbols:    map[string]string{},
			bindings:   map[string]binding{},
			reachable:  map[string]bool{},
		}

		states = append(states, state)
		imports.byPath[file] = state
		for at := dir; at != "." && !imports.dirs[at]; at = path.Dir(at) {
			imports.dirs[at] = true
		}

		if declaredIn[dir] == nil {
			declaredIn[dir] = map[string]map[string]bool{}
		}

		for _, declaration := range state.declarations {
			if declaration.owner == nil {
				if declaredIn[dir][declaration.name] == nil {
					declaredIn[dir][declaration.name] = map[string]bool{}
				}

				declaredIn[dir][declaration.name][file] = true
			}
		}
	}

	imports.roots = sourceRoots(found, imports.dirs)
	imports.packages = map[string][]string{}
	for file := range imports.byPath {
		if path.Base(file) == "__init__.py" {
			dir := path.Dir(file)
			imports.packages[path.Base(dir)] = append(imports.packages[path.Base(dir)], path.Dir(dir))
		}
	}
	qualify := func(state *fileState, name string) string {
		if len(declaredIn[path.Dir(state.path)][name]) < 2 {
			return name
		}

		return strings.TrimSuffix(path.Base(state.path), ".py") + "/" + name
	}

	// A package is an entry point when every file in it is a script, run
	// rather than imported: __main__.py, or one guarded by
	// if __name__ == "__main__". A library with one such guard is still
	// imported.
	scripts := map[string]bool{}
	for _, state := range states {
		dir := path.Dir(state.path)
		script := state.main || path.Base(state.path) == "__main__.py"
		if seen, ok := scripts[dir]; ok {
			scripts[dir] = seen && script
		} else {
			scripts[dir] = script
		}
	}

	var order []*graph.Symbol
	declared := map[*graph.Symbol][]source{}
	for _, state := range states {
		dir := path.Dir(state.path)
		module := result.AddModule(&graph.Module{ID: state.module, Language: Language, Path: dir, Private: private(dir)})
		if manifest := found.manifests[dir]; manifest != "" && module.Manifest == "" {
			module.Manifest, module.Name = manifest, projectName(root, manifest)
		}

		module.Files = append(module.Files, state.path)
		module.Entry = scripts[dir]
		for _, declaration := range state.declarations {
			name, parent := qualify(state, declaration.name), ""
			if declaration.owner != nil {
				parent = qualify(state, declaration.owner.name)
				name = parent + "." + strings.TrimPrefix(declaration.name, declaration.owner.name+".")
			}

			candidate := &graph.Symbol{
				ID:        graph.SymbolID(state.module, name),
				Module:    state.module,
				Name:      name,
				Parent:    parent,
				Kind:      declaration.kind,
				File:      state.path,
				Line:      declaration.line,
				EndLine:   declaration.endLine,
				Contract:  contract(declaration),
				Signature: declaration.signature,
				Fields:    fields(declaration),
				Hash:      hash(declaration.hashText),
				Doc:       declaration.doc,
				DocLine:   declaration.docLine,
			}

			if candidate.Doc == "" {
				candidate.Doc, candidate.DocLine = docComment(state.lines, declaration.commentLine)
			}

			symbol := result.AddSymbol(candidate)
			if symbol == candidate {
				order = append(order, symbol)
			} else {
				// The same name defined again in one file is an overload, a
				// property's setter, or a definition chosen at import time:
				// one symbol with several definitions.
				symbol.Variants = append(symbol.Variants, graph.Variant{File: state.path, Line: declaration.line, Signature: declaration.signature, Fields: candidate.Fields})
				symbol.Hash = hash(symbol.Hash + " " + candidate.Hash)
			}

			declared[symbol] = append(declared[symbol], source{declaration, state})
			if declaration.owner == nil && state.symbols[declaration.name] == "" {
				state.symbols[declaration.name] = symbol.ID
			}
		}
	}

	for _, state := range states {
		state.resolveImports(result, imports)
	}

	methods := map[string][]*graph.Symbol{}
	for _, symbol := range order {
		if symbol.Kind == graph.KindMethod {
			name := symbol.Name[strings.LastIndex(symbol.Name, ".")+1:]
			methods[name] = append(methods[name], symbol)
		}
	}

	for _, symbol := range order {
		for _, each := range declared[symbol] {
			each.state.references(result, imports, symbol, each.declaration, methods)
		}
	}

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

// export finds the symbol a file provides under a name: its own
// definition, a name it imports, or a public name a star import brings in.
func (this *fileState) export(name string, seen map[string]bool) string {
	if this == nil {
		return ""
	}

	key := this.path + "\x00" + name
	if seen[key] {
		return ""
	}

	seen[key] = true
	if id := this.symbols[name]; id != "" {
		return id
	}

	if bound, ok := this.bindings[name]; ok {
		if bound.name == "" || bound.at.file == nil {
			return ""
		}

		return bound.at.file.export(bound.name, seen)
	}

	return this.starredFrom(name, seen)
}

// starred finds the symbol a star import brings in under a name.
func (this *fileState) starred(name string) string {
	return this.starredFrom(name, map[string]bool{})
}

func (this *fileState) starredFrom(name string, seen map[string]bool) string {
	for _, star := range this.stars {
		if star.provides(name) {
			if found := star.export(name, seen); found != "" {
				return found
			}
		}
	}

	return ""
}

// provides reports whether from module import * brings in a name: those
// __all__ lists, else every public name.
func (this *fileState) provides(name string) bool {
	if this.hasAll {
		return slices.Contains(this.all, name)
	}

	return !strings.HasPrefix(name, "_")
}

// references records an edge for every identifier in a declaration that
// names another symbol in the graph.
//
// Notes:
//   - module.name resolves through import bindings, walking packages and
//     submodules as in a.b.c.name; self.name and cls.name through the
//     enclosing class.
//   - x.method(...) with an unknown x resolves only when exactly one method
//     of that name exists in this module or the modules it imports.
func (this *fileState) references(g *graph.Graph, imports *resolver, symbol *graph.Symbol, declaration *declaration, methods map[string][]*graph.Symbol) {
	edge := func(target string, at token, kind string) {
		other := g.Symbol(target)
		if target == "" || other == nil || other.ID == symbol.ID || (symbol.Parent != "" && other.Name == symbol.Parent) {
			return
		}

		if kind == "" {
			kind = graph.EdgeTypeUse
			if other.Kind == graph.KindFunction || other.Kind == graph.KindMethod {
				kind = graph.EdgeCall
			}
		}

		g.AddEdge(graph.Edge{From: symbol.ID, To: target, Kind: kind, File: this.path, Line: at.line})
	}

	for _, base := range declaration.bases {
		edge(this.typeTarget(imports, base), base[0], graph.EdgeEmbeds)
	}

	owner := symbol.Parent
	if owner == "" && (symbol.Kind == graph.KindType || symbol.Kind == graph.KindInterface) {
		owner = symbol.Name
	}

	tokens := declaration.refs
	at := func(index int) token {
		if index >= 0 && index < len(tokens) {
			return tokens[index]
		}

		return token{kind: tokenOperator}
	}

	for index := 0; index < len(tokens); index++ {
		current := tokens[index]
		if (current.is("self") || current.is("cls")) && owner != "" && at(index+1).is(".") && at(index+2).kind == tokenIdent {
			edge(graph.SymbolID(this.module, owner+"."+at(index+2).text), current, "")
			index += 2
			continue
		}

		if current.kind != tokenIdent || isKeyword(current.text) {
			continue
		}

		previous, next := at(index-1), at(index+1)
		switch {
		case previous.is("."):
			if next.is("(") {
				edge(this.uniqueMethod(methods[current.text]), current, graph.EdgeCall)
			}
		case (next.is("=") || next.is(":")) && (previous.is("(") || previous.is(",")):
		case this.symbols[current.text] != "":
			edge(this.symbols[current.text], current, "")
		default:
			bound, ok := this.bindings[current.text]
			switch {
			case !ok:
				edge(this.starred(current.text), current, "")
			case bound.name != "":
				if bound.at.file != nil {
					edge(bound.at.file.export(bound.name, map[string]bool{}), current, "")
				}
			default:
				target, consumed := this.walk(imports, bound.at, tokens, index)
				edge(target, current, "")
				index += consumed
			}
		}
	}
}

// resolveImports places each import in the tree, records an import of
// every other module it reaches, and binds the names it brings in.
func (this *fileState) resolveImports(g *graph.Graph, imports *resolver) {
	record := func(at location, line int) {
		target := graph.ModuleID(Language, at.path)
		if at.file != nil {
			target = at.file.module
		}

		if target != this.module {
			g.AddImport(graph.Import{From: this.module, To: target, File: this.path, Line: line})
			this.reachable[target] = true
		}
	}

	for _, spec := range this.imports {
		if spec.plain {
			at, ok := imports.absolute(spec.module, this.path)
			if !ok {
				continue
			}

			record(at, spec.line)
			if spec.as != "" {
				this.bindings[spec.as] = binding{at: at}
				continue
			}

			first, _, _ := strings.Cut(spec.module, ".")
			if top, ok := imports.absolute(first, this.path); ok {
				this.bindings[first] = binding{at: top}
			}

			continue
		}

		var base location
		var ok bool
		if spec.level > 0 {
			base, ok = imports.relative(this.path, spec.level, spec.module)
		} else {
			base, ok = imports.absolute(spec.module, this.path)
		}

		if !ok {
			continue
		}

		if spec.star {
			record(base, spec.line)
			if base.file != nil {
				this.stars = append(this.stars, base.file)
			}
		}

		recorded := false
		for _, name := range spec.names {
			if sub, ok := imports.locate(path.Join(base.path, name.name)); ok {
				record(sub, spec.line)
				this.bindings[name.as] = binding{at: sub}
				continue
			}

			if !recorded {
				record(base, spec.line)
				recorded = true
			}

			this.bindings[name.as] = binding{at: base, name: name.name}
		}
	}
}

// typeTarget resolves the class a base names.
func (this *fileState) typeTarget(imports *resolver, base []token) string {
	if len(base) == 0 || base[0].kind != tokenIdent {
		return ""
	}

	name := base[0].text
	if id := this.symbols[name]; id != "" {
		return id
	}

	bound, ok := this.bindings[name]
	switch {
	case !ok:
		return this.starred(name)
	case bound.name != "":
		return bound.at.file.export(bound.name, map[string]bool{})
	default:
		target, _ := this.walk(imports, bound.at, base, 0)
		return target
	}
}

func (this *fileState) uniqueMethod(candidates []*graph.Symbol) string {
	var found []string
	for _, candidate := range candidates {
		if candidate.Module == this.module || this.reachable[candidate.Module] {
			found = append(found, candidate.ID)
		}
	}

	if len(found) != 1 {
		return ""
	}

	return found[0]
}

// walk follows an attribute chain from a module, as in a.b.c.name: each
// name is a symbol the module provides, ending the walk, or a submodule to
// continue through.
//
// Returns:
//   - target: the symbol reached, or empty.
//   - consumed: how many tokens after index the chain used.
func (this *fileState) walk(imports *resolver, at location, tokens []token, index int) (target string, consumed int) {
	for index+consumed+2 < len(tokens) && tokens[index+consumed+1].is(".") && tokens[index+consumed+2].kind == tokenIdent {
		name := tokens[index+consumed+2].text
		consumed += 2
		if at.file != nil {
			if found := at.file.export(name, map[string]bool{}); found != "" {
				return found, consumed
			}
		}

		next, ok := imports.locate(path.Join(at.path, name))
		if !ok {
			return "", consumed
		}

		at = next
	}

	return "", consumed
}

// absolute finds the module a dotted name imports, trying the source roots
// that hold the importing file first.
//
// Notes:
//   - A tree that puts a directory on sys.path at run time, as a plugin
//     vendoring its dependencies does, has packages no source root reaches.
//     When the top package's name is a package in exactly one place, the
//     import resolves there; with several candidates it does not resolve.
func (this *resolver) absolute(dotted, from string) (location, bool) {
	relative := strings.ReplaceAll(dotted, ".", "/")
	roots := append([]string{}, this.roots...)
	sort.SliceStable(roots, func(i, j int) bool { return holds(roots[i], from) && !holds(roots[j], from) })
	for _, root := range roots {
		if found, ok := this.locate(path.Join(root, relative)); ok {
			return found, true
		}
	}

	first, _, _ := strings.Cut(dotted, ".")
	if parents := this.packages[first]; len(parents) == 1 {
		return this.locate(path.Join(parents[0], relative))
	}

	return location{}, false
}

// locate finds the module at a path: name.py, name/__init__.py, or a
// directory of Python files with no __init__.py.
func (this *resolver) locate(name string) (location, bool) {
	name = path.Clean(name)
	if name == ".." || strings.HasPrefix(name, "../") {
		return location{}, false
	}

	if state := this.byPath[name+".py"]; state != nil {
		return location{path: name, file: state}, true
	}

	if state := this.byPath[path.Join(name, "__init__.py")]; state != nil {
		return location{path: name, file: state}, true
	}

	if this.dirs[name] {
		return location{path: name}, true
	}

	return location{}, false
}

// relative finds the module a relative import names: one dot is the
// importing file's package, each further dot its parent.
func (this *resolver) relative(from string, level int, module string) (location, bool) {
	base := path.Dir(from)
	for range level - 1 {
		if base == "." {
			return location{}, false
		}

		base = path.Dir(base)
	}

	if module != "" {
		base = path.Join(base, strings.ReplaceAll(module, ".", "/"))
	}

	return this.locate(base)
}

// contract reports whether a declaration is part of its module's contract:
// a public name, or a public member of a public class.
func contract(declaration *declaration) bool {
	if declaration.owner == nil {
		return public(declaration.name)
	}

	_, member, _ := strings.Cut(declaration.name, ".")
	return public(declaration.owner.name) && public(member)
}

// docComment reads the # comment that ends on the line just above a
// declaration, for those without a docstring.
func docComment(lines []string, line int) (text string, start int) {
	var result []string
	for index := line - 2; index >= 0 && index < len(lines); index-- {
		current := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(current, "#") || strings.HasPrefix(current, "#!") || strings.HasPrefix(current, "# type:") || strings.HasPrefix(current, "# noqa") || strings.HasPrefix(current, "# pragma") {
			break
		}

		start = index + 1
		result = append([]string{strings.TrimPrefix(strings.TrimPrefix(current, "#"), " ")}, result...)
	}

	text = strings.TrimSpace(strings.Join(result, "\n"))
	if text == "" {
		return "", 0
	}

	return text, start
}

// fields lists a class's fields, each a contract when the class is public
// and the field's name is.
func fields(declaration *declaration) []graph.Field {
	var result []graph.Field
	for _, field := range declaration.fields {
		field.Contract = public(declaration.name) && public(fieldName(field.Text))
		result = append(result, field)
	}

	return result
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(collapse(text)))
	return hex.EncodeToString(sum[:6])
}

// holds reports whether a source root contains a file.
func holds(root, file string) bool {
	return root == "." || strings.HasPrefix(file, root+"/")
}

// private reports whether a directory is private by Python's convention:
// some element of its path starts with an underscore.
func private(dir string) bool {
	for _, part := range strings.Split(dir, "/") {
		if strings.HasPrefix(part, "_") && !dunder(part) {
			return true
		}
	}

	return false
}

// projectName reads the project name a pyproject.toml declares under
// [project] or [tool.poetry], or empty for other manifests.
func projectName(root, manifest string) string {
	if path.Base(manifest) != "pyproject.toml" {
		return ""
	}

	file, err := os.Open(filepath.Join(root, filepath.FromSlash(manifest)))
	if err != nil {
		return ""
	}

	defer func() { _ = file.Close() }()
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		current := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(current, "[") {
			section = current
			continue
		}

		key, value, found := strings.Cut(current, "=")
		if found && strings.TrimSpace(key) == "name" && (section == "[project]" || section == "[tool.poetry]") {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}

	return ""
}

// skippedDir reports whether a directory holds tests, caches, dependencies
// or build output rather than source.
func skippedDir(current, name string) bool {
	switch name {
	case "__pycache__", "node_modules", "site-packages", "venv", "env", "build", "dist", "testdata", "tests", "test":
		return true
	}

	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") {
		return true
	}

	_, err := os.Stat(filepath.Join(current, "pyvenv.cfg"))
	return err == nil
}

// isSource reports whether a file is Python source: not a stub or a test.
func isSource(name string) bool {
	return strings.HasSuffix(name, ".py") && !strings.HasPrefix(name, "test_") && !strings.HasSuffix(name, "_test.py") && name != "conftest.py"
}

// sourceRoots lists where absolute imports start: the repository root, src
// when it holds packages, and each project's directory and its src.
func sourceRoots(found tree, dirs map[string]bool) []string {
	seen := map[string]bool{".": true}
	result := []string{"."}
	add := func(dir string) {
		if (dir == "." || dirs[dir]) && !seen[dir] {
			seen[dir] = true
			result = append(result, dir)
		}
	}

	add("src")
	var projects []string
	for dir := range found.manifests {
		projects = append(projects, dir)
	}

	sort.Strings(projects)
	for _, dir := range projects {
		add(dir)
		add(path.Join(dir, "src"))
	}

	sort.SliceStable(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result
}

// walk lists every Python source file under root, with the project
// manifest of each directory that has one.
func walk(root string) (result tree, err error) {
	result = tree{manifests: map[string]string{}}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relative, _ := filepath.Rel(root, current)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if current != root && skippedDir(current, entry.Name()) {
				return filepath.SkipDir
			}

			for _, name := range manifestNames {
				if _, err := os.Stat(filepath.Join(current, name)); err == nil {
					result.manifests[relative] = path.Join(relative, name)
					break
				}
			}

			return nil
		}

		if isSource(entry.Name()) {
			result.files = append(result.files, relative)
		}

		return nil
	})

	sort.Strings(result.files)
	return result, err
}
