package golang

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
	path    string
	module  string
	imports map[string]string
	aliases map[string]bool
}

// NewExtractor creates a Go extractor.
//
// Returns:
//   - result: the extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract reads every non-test Go file under root into one graph. A module
// is a directory. Directories holding their own go.mod are separate Go
// modules and are left out, as the go command leaves them out.
//
// Parameters:
//   - root: the directory to read.
//
// Returns:
//   - result: the normalized graph, without layers.
//   - err: a file could not be read.
func (this *Extractor) Extract(root string) (result *graph.Graph, err error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}

	modulePath := readGoModule(root)
	result = graph.New()
	var states []*fileState
	packages := map[string]string{}
	for _, relative := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, err
		}

		dir := path.Dir(relative)
		state := &fileState{sourceFile: parseFile(src), path: relative, module: graph.ModuleID("go", dir), imports: map[string]string{}, aliases: map[string]bool{}}
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
		module := result.AddModule(&graph.Module{ID: state.module, Language: "go", Path: dir, Name: packages[dir], Entry: packages[dir] == "main"})
		module.Files = append(module.Files, state.path)
		if moduleImports[state.module] == nil {
			moduleImports[state.module] = map[string]bool{}
		}

		state.resolveImports(modulePath, packages, moduleImports[state.module])
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

			symbol := result.AddSymbol(candidate)
			if symbol == candidate {
				order = append(order, symbol)
			} else {
				// The same name declared twice in one package is a build
				// variant: each file compiles under different constraints.
				symbol.Variants = append(symbol.Variants, graph.Variant{File: state.path, Line: declaration.line, Signature: declaration.signature, Fields: declaration.fields})
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
	edge := func(target string, at token, kind string) {
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
	}

	for _, embedded := range declaration.embedded {
		if target := this.typeTarget(embedded); target != "" {
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
		case selects && declaration.receiver != "" && current.text == declaration.receiver && g.Symbol(graph.SymbolID(this.module, symbol.Parent+"."+tokens[index+2].text)) != nil:
			edge(graph.SymbolID(this.module, symbol.Parent+"."+tokens[index+2].text), current, "")
			index += 2
		case !inCase && index+1 < len(tokens) && tokens[index+1].is(":"):
		default:
			edge(graph.SymbolID(this.module, current.text), current, "")
		}
	}
}

func (this *fileState) resolveImports(modulePath string, packages map[string]string, moduleImports map[string]bool) {
	for _, spec := range this.sourceFile.imports {
		alias := spec.alias
		inRepo := modulePath != "" && (spec.path == modulePath || strings.HasPrefix(spec.path, modulePath+"/"))
		dir := strings.TrimPrefix(spec.path, modulePath+"/")
		if spec.path == modulePath {
			dir = "."
		}
		if alias == "" {
			alias = path.Base(spec.path)
			if inRepo && packages[dir] != "" {
				alias = packages[dir]
			}
		}

		if alias == "_" || alias == "." {
			continue
		}

		this.aliases[alias] = true
		if inRepo {
			target := graph.ModuleID("go", dir)
			this.imports[alias] = target
			moduleImports[target] = true
		}
	}
}

func (this *fileState) typeTarget(tokens []token) string {
	if len(tokens) > 0 && tokens[0].is("*") {
		tokens = tokens[1:]
	}

	switch {
	case len(tokens) >= 3 && tokens[1].is("."):
		if module := this.imports[tokens[0].text]; module != "" {
			return graph.SymbolID(module, tokens[2].text)
		}
	case len(tokens) >= 1:
		return graph.SymbolID(this.module, tokens[0].text)
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

func contract(declaration *declaration) bool {
	_, name, found := strings.Cut(declaration.name, ".")
	if !found {
		return exported(declaration.name)
	}

	return exported(name) && exported(declaration.parent)
}

func goFiles(root string) ([]string, error) {
	var result []string
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		name := entry.Name()
		if entry.IsDir() {
			if current != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}

			// A directory with its own go.mod is a separate module, as the go
			// command sees it: code that uses this one from outside.
			if _, err := os.Stat(filepath.Join(current, "go.mod")); current != root && err == nil {
				return filepath.SkipDir
			}

			return nil
		}

		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			relative, _ := filepath.Rel(root, current)
			result = append(result, filepath.ToSlash(relative))
		}

		return nil
	})

	sort.Strings(result)
	return result, err
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

func readGoModule(root string) string {
	file, err := os.Open(filepath.Join(root, "go.mod"))
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

// signatureKey parses a CML function line and returns its comparison key,
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
