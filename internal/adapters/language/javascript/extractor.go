package javascript

import (
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

const (
	LanguageJavaScript = "js"
	LanguageTypeScript = "ts"
)

// Extractor builds contract graphs from JavaScript and TypeScript source
// trees.
type Extractor struct{}

// fileState is one parsed file placed in its module.
type fileState struct {
	*sourceFile
	path      string
	module    string
	lines     []string
	symbols   map[string]string
	bindings  map[string]importBinding
	reexports []reexport
	reachable map[string]bool
}

// importBinding is a local name bound to another file's export.
type importBinding struct {
	file     *fileState
	imported string
	require  bool
}

// reexport is an export ... from: names, or every name, another file
// exports.
type reexport struct {
	file  *fileState
	names []binding
	all   bool
}

// source is one declaration of a symbol and the file it came from.
type source struct {
	declaration *declaration
	state       *fileState
}

// tree is everything the walk finds: source files, package manifests and
// compiler configs, each by repository-relative path.
type tree struct {
	files     []string
	manifests map[string]*manifest
	configs   map[string]*compilerConfig
}

// NewExtractor creates a JavaScript and TypeScript extractor.
//
// Returns:
//   - result: the extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract reads every non-test JavaScript and TypeScript file under root
// into one graph. A module is a directory: ts when it holds any TypeScript
// file, js otherwise.
//
// Notes:
//   - JavaScript scopes names to a file, but a module is a directory. A
//     top-level name declared in more than one file of a directory is
//     qualified by its file's name, as in button/render, or as in
//     button_mjs/render when button.ts sits beside it.
//   - Declaration files (.d.ts), tests (.test., .spec., __tests__) and
//     minified files are skipped, as are node_modules, dist, build and
//     coverage directories.
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

	languages := map[string]string{}
	for _, file := range found.files {
		dir := path.Dir(file)
		if typeScript(file) {
			languages[dir] = LanguageTypeScript
		} else if languages[dir] == "" {
			languages[dir] = LanguageJavaScript
		}
	}

	result = graph.New()
	var states []*fileState
	byPath := map[string]*fileState{}
	declaredIn := map[string]map[string]map[string]bool{}
	for _, file := range found.files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, err
		}

		dir := path.Dir(file)
		state := &fileState{
			sourceFile: parseFile(src, !typeScript(file) || strings.HasSuffix(file, ".tsx")),
			path:       file,
			module:     graph.ModuleID(languages[dir], dir),
			lines:      strings.Split(string(src), "\n"),
			symbols:    map[string]string{},
			bindings:   map[string]importBinding{},
			reachable:  map[string]bool{},
		}

		states = append(states, state)
		byPath[file] = state
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

	stems := map[string]int{}
	for _, file := range found.files {
		stems[path.Join(path.Dir(file), stem(file))]++
	}

	// A qualifier is the file's name without its extensions, or with its
	// dots made underscores when another file in the directory shares that
	// stem, as a.ts and a.mjs do.
	qualify := func(state *fileState, name string) string {
		if len(declaredIn[path.Dir(state.path)][name]) < 2 {
			return name
		}

		qualifier := stem(state.path)
		if stems[path.Join(path.Dir(state.path), qualifier)] > 1 {
			qualifier = strings.ReplaceAll(path.Base(state.path), ".", "_")
		}

		return qualifier + "/" + name
	}

	var order []*graph.Symbol
	declared := map[*graph.Symbol][]source{}
	for _, state := range states {
		dir := path.Dir(state.path)
		module := &graph.Module{ID: state.module, Language: languages[dir], Path: dir, Private: found.private(dir)}
		if each := found.manifests[dir]; each != nil {
			module.Name, module.Manifest = each.name, path.Join(dir, "package.json")
		}

		module = result.AddModule(module)
		module.Files = append(module.Files, state.path)
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
			}

			candidate.Doc, candidate.DocLine = docComment(state.lines, max(declaration.docLine, 1))
			symbol := result.AddSymbol(candidate)
			if symbol == candidate {
				order = append(order, symbol)
			} else {
				// The same name declared again in one file is an overload or
				// an accessor pair: one symbol with several declarations.
				symbol.Variants = append(symbol.Variants, graph.Variant{File: state.path, Line: declaration.line, Signature: declaration.signature, Fields: candidate.Fields})
				symbol.Hash = hash(symbol.Hash + " " + candidate.Hash)
			}

			declared[symbol] = append(declared[symbol], source{declaration, state})
			if declaration.owner == nil && state.symbols[declaration.name] == "" {
				state.symbols[declaration.name] = symbol.ID
			}
		}
	}

	imports := newResolver(found.files, found.manifests, found.configs)
	for _, state := range states {
		state.resolveImports(result, imports, byPath)
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
			each.state.references(result, symbol, each.declaration, methods)
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

// export finds the symbol a file exports under a name, following export
// lists, imports and re-exports, or empty when there is none.
func (this *fileState) export(name string, seen map[string]bool) string {
	key := this.path + "\x00" + name
	if seen[key] {
		return ""
	}

	seen[key] = true
	if local, ok := this.exports[name]; ok {
		return this.lookup(local, seen)
	}

	for _, each := range this.reexports {
		for _, item := range each.names {
			if item.local == name && item.imported != "*" {
				return each.file.export(item.imported, seen)
			}
		}
	}

	if name == "default" {
		return ""
	}

	for _, each := range this.reexports {
		if each.all {
			if found := each.file.export(name, seen); found != "" {
				return found
			}
		}
	}

	return ""
}

// lookup resolves a name in a file's scope: its own declaration, else the
// export an import binds it to.
func (this *fileState) lookup(name string, seen map[string]bool) string {
	if id := this.symbols[name]; id != "" {
		return id
	}

	if bound, ok := this.bindings[name]; ok && bound.imported != "*" {
		return bound.file.export(bound.imported, seen)
	}

	return ""
}

// references records an edge for every identifier in a declaration that
// names another symbol in the graph.
//
// Notes:
//   - ns.Name resolves through a namespace import or require; this.name
//     through the enclosing class.
//   - x.method(...) with an unknown x resolves only when exactly one method
//     of that name exists in this module or the modules it imports.
func (this *fileState) references(g *graph.Graph, symbol *graph.Symbol, declaration *declaration, methods map[string][]*graph.Symbol) {
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

	for _, each := range declaration.extends {
		if len(each) > 0 {
			edge(this.typeTarget(each), each[0], graph.EdgeEmbeds)
		}
	}

	for _, each := range declaration.implements {
		if len(each) > 0 {
			edge(this.typeTarget(each), each[0], graph.EdgeImplements)
		}
	}

	owner := symbol.Parent
	if owner == "" && symbol.Kind == graph.KindType {
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
		if current.is("this") && owner != "" && at(index+1).is(".") && at(index+2).kind == tokenIdent {
			edge(graph.SymbolID(this.module, owner+"."+at(index+2).text), current, "")
			index += 2
			continue
		}

		if current.kind != tokenIdent || isKeyword(current.text) {
			continue
		}

		previous, next := at(index-1), at(index+1)
		switch {
		case previous.is(".") || previous.is("?."):
			if next.is("(") {
				edge(this.uniqueMethod(methods[current.text]), current, graph.EdgeCall)
			}
		case (next.is(":") || next.is("?") && at(index+2).is(":")) && (previous.is("{") || previous.is(",") || previous.is("(")):
		default:
			bound, imported := this.bindings[current.text]
			switch {
			case this.symbols[current.text] != "":
				edge(this.symbols[current.text], current, "")
			case !imported:
			case bound.imported != "*":
				edge(bound.file.export(bound.imported, map[string]bool{}), current, "")
			case next.is(".") && at(index+2).kind == tokenIdent:
				edge(bound.file.export(at(index+2).text, map[string]bool{}), current, "")
				index += 2
			case bound.require:
				edge(bound.file.export("default", map[string]bool{}), current, "")
			}
		}
	}
}

// resolveImports places each import in the tree, records an import of
// every other module it reaches, and binds the names it brings in.
func (this *fileState) resolveImports(g *graph.Graph, imports *resolver, byPath map[string]*fileState) {
	for _, spec := range this.imports {
		found, ok := imports.resolve(this.path, spec.path)
		if !ok {
			continue
		}

		target := byPath[found]
		if target.module != this.module {
			g.AddImport(graph.Import{From: this.module, To: target.module, File: this.path, Line: spec.line})
			this.reachable[target.module] = true
		}

		if spec.reexport {
			this.reexports = append(this.reexports, reexport{file: target, names: spec.names, all: spec.all})
			continue
		}

		for _, name := range spec.names {
			this.bindings[name.local] = importBinding{file: target, imported: name.imported, require: spec.require}
		}
	}
}

// typeTarget resolves the type an extends or implements clause names.
func (this *fileState) typeTarget(tokens []token) string {
	if tokens[0].kind != tokenIdent {
		return ""
	}

	if bound, ok := this.bindings[tokens[0].text]; ok && bound.imported == "*" && len(tokens) >= 3 && tokens[1].is(".") {
		return bound.file.export(tokens[2].text, map[string]bool{})
	}

	return this.lookup(tokens[0].text, map[string]bool{})
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

// contract reports whether a declaration is part of its module's contract:
// exported, or a member of an exported class or interface that is not
// private.
func contract(declaration *declaration) bool {
	if declaration.owner == nil {
		return declaration.exported
	}

	return declaration.owner.exported && !declaration.private
}

// docComment reads the comment that ends on the line just above a
// declaration, such as a JSDoc block, without its comment markers. Tool
// directives such as // @ts-ignore and // eslint-disable are not
// documentation.
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
				text = strings.TrimPrefix(text[opening+2:], "*")
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimSuffix(text, "*/")), "*"))
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

// directive reports whether a line comment is a tool directive rather than
// documentation.
func directive(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(text, "//"))
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "@ts-") || strings.HasPrefix(text, "eslint-") || strings.HasPrefix(text, "prettier-ignore") || strings.HasPrefix(text, "#region") || strings.HasPrefix(text, "#endregion")
}

// fields lists a declaration's fields, each a contract when its owner is
// exported and the field is not private.
func fields(declaration *declaration) []graph.Field {
	var result []graph.Field
	for index, field := range declaration.fields {
		field.Contract = declaration.exported && !declaration.fieldPrivate[index]
		result = append(result, field)
	}

	return result
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(collapse(text)))
	return hex.EncodeToString(sum[:6])
}

// private reports whether a directory belongs to a private package: the
// nearest package.json at or above it says "private": true, so nothing
// outside this repository can install and import it.
func (this tree) private(dir string) bool {
	for {
		if each := this.manifests[dir]; each != nil {
			return each.private
		}

		if dir == "." || dir == "" {
			return false
		}

		dir = path.Dir(dir)
	}
}

// skippedDir reports whether a directory holds dependencies, build output,
// tests or tooling rather than source.
func skippedDir(name string) bool {
	switch name {
	case "node_modules", "bower_components", "vendor", "testdata", "dist", "build", "coverage":
		return true
	}

	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// isSource reports whether a file name is JavaScript or TypeScript
// source: not a declaration file, test or minified bundle.
func isSource(name string) bool {
	extension := path.Ext(name)
	if !slices.Contains(sourceExtensions, extension) {
		return false
	}

	stem := strings.TrimSuffix(name, extension)
	return !strings.HasSuffix(stem, ".d") && !strings.HasSuffix(stem, ".min") && !strings.Contains(name, ".test.") && !strings.Contains(name, ".spec.")
}

// stem is a file's name without any of its extensions.
func stem(file string) string {
	base := path.Base(file)
	return base[:strings.Index(base+".", ".")]
}

// typeScript reports whether a file is TypeScript.
func typeScript(name string) bool {
	switch path.Ext(name) {
	case ".ts", ".tsx", ".mts", ".cts":
		return true
	}

	return false
}

// walk lists every source file under root, with the package.json and
// compiler config of each directory that has one.
func walk(root string) (result tree, err error) {
	result = tree{manifests: map[string]*manifest{}, configs: map[string]*compilerConfig{}}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relative, _ := filepath.Rel(root, current)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if current != root && skippedDir(entry.Name()) {
				return filepath.SkipDir
			}

			if each := readManifest(root, relative); each != nil {
				result.manifests[relative] = each
			}

			if config := readCompilerConfig(root, relative); config != nil {
				result.configs[relative] = config
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
