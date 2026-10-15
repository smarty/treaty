package javascript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

var (
	scriptLang  = regexp.MustCompile(`\blang\s*=\s*["']?tsx?\b`)
	templateTag = regexp.MustCompile(`<([A-Z][A-Za-z0-9]*)`)

	// kebabTag is a component tag in kebab case, such as <my-widget>, which
	// Vue reads as MyWidget.
	kebabTag = regexp.MustCompile(`<([a-z][a-z0-9]*(?:-[a-z0-9]+)+)`)
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
	aliases   *aliases
}

// importBinding is a local name bound to another file's export.
type importBinding struct {
	file     *fileState
	imported string
	require  bool
	line     int
}

// reexport is an export ... from: names, or every name, another file
// exports.
type reexport struct {
	file  *fileState
	names []binding
	all   bool
	line  int
}

// aliases holds the symbols made for names a file exports from another
// module, so a consumer depends on the module it imported, such as a
// context's public index, rather than the one behind it.
type aliases struct {
	graph *graph.Graph
	made  map[string]string
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
//   - A Vue or Svelte component is read through its <script> blocks, as
//     TypeScript when one has lang="ts".
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

	sources, scripts, typed := map[string][]byte{}, map[string][]byte{}, map[string]bool{}
	languages := map[string]string{}
	for _, file := range found.files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, err
		}

		sources[file], scripts[file], typed[file] = src, src, typeScript(file)
		if component(file) {
			scripts[file], typed[file] = componentScript(src)
		}

		dir := path.Dir(file)
		if typed[file] {
			languages[dir] = LanguageTypeScript
		} else if languages[dir] == "" {
			languages[dir] = LanguageJavaScript
		}
	}

	result = graph.New()
	made := &aliases{graph: result, made: map[string]string{}}
	var states []*fileState
	byPath := map[string]*fileState{}
	declaredIn := map[string]map[string]map[string]bool{}
	for _, file := range found.files {
		src := sources[file]
		dir := path.Dir(file)
		state := &fileState{
			sourceFile: parseFile(scripts[file], !typed[file] || strings.HasSuffix(file, ".tsx")),
			path:       file,
			module:     graph.ModuleID(languages[dir], dir),
			lines:      strings.Split(string(src), "\n"),
			symbols:    map[string]string{},
			bindings:   map[string]importBinding{},
			reachable:  map[string]bool{},
			aliases:    made,
		}

		if component(file) && state.exports["default"] == "" {
			state.declarations = append(state.declarations, componentDeclaration(file, src, scripts[file], typed[file]))
			state.exports["default"] = state.declarations[len(state.declarations)-1].name
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

	// Every name a file exports from another module by name is part of its
	// module's contract, used or not. A star re-export's names are made as
	// consumers use them.
	for _, state := range states {
		for _, name := range state.exportedNames() {
			state.export(name, map[string]bool{})
		}
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
// lists, imports and re-exports, or empty when there is none. A name the
// file exports from another module resolves to an alias in the file's own
// module, which uses the symbol behind it.
func (this *fileState) export(name string, seen map[string]bool) string {
	key := this.path + "\x00" + name
	if seen[key] {
		return ""
	}

	seen[key] = true
	found, line := this.exported(name, seen)
	return this.alias(name, found, line)
}

// exported finds the symbol behind a name the file exports, and the line
// that brings it in from another file, if one does.
func (this *fileState) exported(name string, seen map[string]bool) (id string, line int) {
	if local, ok := this.exports[name]; ok {
		return this.lookup(local, seen), this.bindings[local].line
	}

	for _, each := range this.reexports {
		for _, item := range each.names {
			if item.local == name && item.imported != "*" {
				return each.file.export(item.imported, seen), each.line
			}
		}
	}

	if name == "default" {
		return "", 0
	}

	for _, each := range this.reexports {
		if each.all {
			if found := each.file.export(name, seen); found != "" {
				return found, each.line
			}
		}
	}

	return "", 0
}

// exportedNames lists the names the file exports by name, its own and
// those it re-exports.
func (this *fileState) exportedNames() (result []string) {
	for name := range this.exports {
		result = append(result, name)
	}

	for _, each := range this.reexports {
		for _, item := range each.names {
			if item.imported != "*" {
				result = append(result, item.local)
			}
		}
	}

	sort.Strings(result)
	return result
}

// alias returns target when it is in the file's module, or else a symbol
// in the file's module that stands for it under the exported name, made on
// first use with an edge to target. It takes the target's kind, signature
// and documentation, so a change behind it shows on the module that
// offers it too.
func (this *fileState) alias(name, target string, line int) string {
	other := this.aliases.graph.Symbol(target)
	if other == nil || other.Module == this.module {
		return target
	}

	key := this.path + "\x00" + name
	if id, ok := this.aliases.made[key]; ok {
		return id
	}

	id := graph.SymbolID(this.module, name)
	if this.aliases.graph.Symbol(id) != nil {
		id = graph.SymbolID(this.module, stem(this.path)+"/"+name)
	}

	symbol := this.aliases.graph.AddSymbol(&graph.Symbol{
		ID: id, Module: this.module, Name: strings.TrimPrefix(id, this.module+":"), Kind: other.Kind,
		File: this.path, Line: max(line, 1), EndLine: max(line, 1), Contract: true,
		Signature: renamed(other.Signature, other.Name, name), Fields: other.Fields, Doc: other.Doc, Hash: hash("alias " + other.ID),
	})

	this.aliases.made[key] = symbol.ID
	kind := graph.EdgeTypeUse
	if other.Kind == graph.KindFunction || other.Kind == graph.KindMethod {
		kind = graph.EdgeCall
	}

	this.aliases.graph.AddEdge(graph.Edge{From: symbol.ID, To: other.ID, Kind: kind, File: this.path, Line: symbol.Line})
	return symbol.ID
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
		if spec.glob {
			for _, found := range imports.glob(this.path, spec.path, spec.exclude) {
				if target := byPath[found]; target.module != this.module {
					g.AddImport(graph.Import{From: this.module, To: target.module, File: this.path, Line: spec.line})
					this.reachable[target.module] = true
				}
			}

			continue
		}

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
			this.reexports = append(this.reexports, reexport{file: target, names: spec.names, all: spec.all, line: spec.line})
			continue
		}

		for _, name := range spec.names {
			this.bindings[name.local] = importBinding{file: target, imported: name.imported, require: spec.require, line: spec.line}
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

// renamed puts an alias's name in place of its target's in a signature,
// as export { a as b } from asks, so the signature still declares the
// alias.
func renamed(signature, target, name string) string {
	target = target[strings.LastIndexAny(target, "/.")+1:]
	if target == name || name == "default" {
		return signature
	}

	if found := regexp.MustCompile(`\b` + regexp.QuoteMeta(target) + `\b`).FindStringIndex(signature); found != nil {
		return signature[:found[0]] + name + signature[found[1]:]
	}

	return signature
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
	if !slices.Contains(sourceExtensions, extension) && !component(name) {
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
// component reports whether a file is a Vue or Svelte single-file
// component, whose script blocks hold its code.
func component(name string) bool {
	return path.Ext(name) == ".vue" || path.Ext(name) == ".svelte"
}

// componentScript keeps a component's <script> blocks and blanks the rest
// of each line, so lines keep their numbers. A block with lang="ts" makes
// the component TypeScript.
func componentScript(src []byte) (script []byte, typed bool) {
	result := make([]byte, 0, len(src))
	lower := strings.ToLower(string(src))
	at := 0
	for {
		open := strings.Index(lower[at:], "<script")
		if open < 0 {
			break
		}

		open += at
		body := strings.Index(lower[open:], ">")
		if body < 0 {
			break
		}

		body += open + 1
		end := strings.Index(lower[body:], "</script")
		if end < 0 {
			end = len(lower) - body
		}

		end += body
		if attributes := lower[open:body]; scriptLang.MatchString(attributes) {
			typed = true
		}

		result = append(result, blank(src[at:body])...)
		result = append(result, src[body:end]...)
		at = end
	}

	return append(result, blank(src[at:])...), typed
}

// componentDeclaration makes the default export of a component whose script
// has none, as with <script setup> or Svelte: a function named after its
// file in PascalCase, spanning the file. It references what its script
// names and the components its template uses by name, in PascalCase or,
// as Vue allows, in kebab case.
func componentDeclaration(file string, src, script []byte, typed bool) *declaration {
	var name strings.Builder
	for _, part := range strings.FieldsFunc(stem(file), func(r rune) bool { return !isLetter(byte(r)) && !(r >= '0' && r <= '9') }) {
		name.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}

	if name.Len() == 0 || name.String()[0] >= '0' && name.String()[0] <= '9' {
		name.Reset()
		name.WriteString("Component")
	}

	result := parseFile([]byte("export default function "+name.String()+"() {}"), false).declarations[0]
	result.line, result.endLine, result.docLine, result.hashText = 1, max(bytes.Count(src, []byte("\n")), 1), 0, string(src)
	result.refs = lex(script, !typed)
	for index, line := range strings.Split(string(src), "\n") {
		for _, tag := range templateTag.FindAllStringSubmatch(line, -1) {
			result.refs = append(result.refs, token{kind: tokenIdent, text: tag[1], line: index + 1})
		}

		for _, tag := range kebabTag.FindAllStringSubmatch(line, -1) {
			result.refs = append(result.refs, token{kind: tokenIdent, text: pascal(tag[1]), line: index + 1})
		}
	}

	return result
}

// pascal turns a kebab-case name, such as my-widget, into PascalCase.
func pascal(kebab string) string {
	var result strings.Builder
	for _, part := range strings.Split(kebab, "-") {
		if part != "" {
			result.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}

	return result.String()
}

// blank keeps only the line breaks of text.
func blank(text []byte) []byte {
	return bytes.Repeat([]byte("\n"), bytes.Count(text, []byte("\n")))
}

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
