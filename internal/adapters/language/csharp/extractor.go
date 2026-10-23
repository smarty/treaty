package csharp

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

// Extractor builds contract graphs from C# source trees.
type Extractor struct {
	skipped []skip
}

// fileState is one parsed file placed in its module and project.
type fileState struct {
	*sourceFile
	path    string
	dir     string
	module  string
	project *project
	lines   []string
}

// tree is everything the walk finds: source files and project files.
type tree struct {
	sources  []string
	projects []string
}

// NewExtractor creates a C# extractor.
//
// Returns:
//   - result: the extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract reads every non-test C# file under root into one graph. A module
// is a directory.
//
// Notes:
//   - Files of test projects, generated files, and bin, obj and hidden
//     directories are skipped.
//   - A file that does not parse, and a project file that is not XML, is
//     skipped and listed by Skipped.
//
// Parameters:
//   - root: the directory to read.
//
// Returns:
//   - result: the normalized graph, without layers.
//   - err: a file could not be read.
func (this *Extractor) Extract(root string) (result *graph.Graph, err error) {
	this.skipped = nil
	found, err := walk(root)
	if err != nil {
		return nil, err
	}

	projects, err := readProjects(root, found.projects)
	if err != nil {
		return nil, err
	}

	this.skipped = append(this.skipped, projects.skipped...)
	var states []*fileState
	for _, file := range found.sources {
		owner := projects.owner(file)
		if owner.test || generatedName(file) {
			continue
		}

		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return nil, err
		}

		if generatedText(src) {
			continue
		}

		parsed, err := parseFile(src)
		if err != nil {
			this.skipped = append(this.skipped, skip{path: file, reason: err.Error()})
			continue
		}

		dir := path.Dir(file)
		states = append(states, &fileState{
			sourceFile: parsed,
			path:       file,
			dir:        dir,
			module:     graph.ModuleID(Language, dir),
			project:    owner,
			lines:      strings.Split(string(src), "\n"),
		})
	}

	result = graph.New()
	for _, state := range states {
		module := result.AddModule(&graph.Module{ID: state.module, Language: Language, Path: state.dir})
		module.Files = append(module.Files, state.path)
		if state.project != projects.implicit && state.project.dir == state.dir {
			module.Manifest, module.Name = state.project.path, state.project.name
		}

		if state.program && projects.entry(state.project) {
			module.Entry = true
		}
	}

	mergePartials(states)
	built := symbols(states, result)
	public := map[string]bool{}
	for _, each := range built.order {
		if built.assembly[each.ID] {
			public[each.Module] = true
		}
	}

	for _, module := range result.Modules {
		module.Private = !public[module.ID]
	}

	references(states, result, built, projects)
	result.Normalize()
	return result, nil
}

// source is one declaration of a symbol and the file it came from.
type source struct {
	declaration *declaration
	state       *fileState
}

// built is the symbols made from the declarations, with what references
// need: each declaration's symbol id and the sources of each symbol.
type built struct {
	order    []*graph.Symbol
	ids      map[*declaration]string
	sources  map[string][]source
	assembly map[string]bool
}

// mergePartials gives every part of a partial type the merged modifiers
// and base list, then sets the access flags again, since a member's flags
// depend on its type's.
func mergePartials(states []*fileState) {
	parts := map[string][]*declaration{}
	var keys []string
	for _, state := range states {
		for _, declaration := range state.declarations {
			if !declaration.partial || (declaration.kind != graph.KindType && declaration.kind != graph.KindInterface) {
				continue
			}

			key := state.project.path + "\x00" + declaration.namespace + "\x00" + declaration.name
			if parts[key] == nil {
				keys = append(keys, key)
			}

			parts[key] = append(parts[key], declaration)
		}
	}

	for _, key := range keys {
		var mods, bases []string
		for _, part := range parts[key] {
			for _, each := range part.modifiers {
				if !slices.Contains(mods, each) {
					mods = append(mods, each)
				}
			}

			for _, each := range part.baseTexts {
				if !slices.Contains(bases, each) {
					bases = append(bases, each)
				}
			}
		}

		sort.Strings(bases)
		for _, part := range parts[key] {
			part.modifiers, part.baseTexts = mods, bases
			part.signature = typeSignature(part)
		}
	}

	for _, state := range states {
		for _, declaration := range state.declarations {
			declaration.assembly, declaration.directory = access(declaration)
		}
	}
}

// symbols adds one symbol for each declared name: the parts of a partial
// type or member make one symbol, and any other repeat of a name is a
// variant.
func symbols(states []*fileState, g *graph.Graph) built {
	result := built{ids: map[*declaration]string{}, sources: map[string][]source{}, assembly: map[string]bool{}}
	qualified := qualifiers(states)
	var ids []string
	modules := map[string]string{}
	for _, state := range states {
		for _, declaration := range state.declarations {
			name, _ := qualifiedName(declaration, state, qualified)
			module := owningModule(states, state, declaration)
			id := graph.SymbolID(module, name)
			if result.sources[id] == nil {
				ids = append(ids, id)
				modules[id] = module
			}

			result.sources[id] = append(result.sources[id], source{declaration, state})
			result.ids[declaration] = id
		}
	}

	for _, id := range ids {
		sources := result.sources[id]
		first := sources[0]
		name, parent := qualifiedName(first.declaration, first.state, qualified)
		chosen := first
		if first.declaration.partial {
			for _, each := range sources {
				if !each.declaration.declaring {
					chosen = each
					break
				}
			}
		}

		declaration := chosen.declaration
		symbol := &graph.Symbol{
			ID:        id,
			Module:    modules[id],
			Name:      name,
			Parent:    parent,
			Kind:      declaration.kind,
			File:      chosen.state.path,
			Line:      declaration.line,
			EndLine:   declaration.endLine,
			Contract:  declaration.directory,
			Signature: declaration.signature,
		}

		var hashed []string
		for _, each := range sources {
			if each.declaration.doc != "" && symbol.Doc == "" {
				symbol.Doc, symbol.DocLine = each.declaration.doc, each.declaration.docLine
				if each.state.path != symbol.File {
					symbol.DocLine = 0
				}
			}

			partial := first.declaration.partial && each.declaration.partial
			if partial || each == first {
				hashed = append(hashed, hashText(each.declaration))
				continue
			}

			symbol.Variants = append(symbol.Variants, graph.Variant{File: each.state.path, Line: each.declaration.line, Signature: each.declaration.signature})
			hashed = append(hashed, hashText(each.declaration))
		}

		symbol.Hash = hash(strings.Join(hashed, " "))
		result.assembly[id] = declaration.assembly
		result.order = append(result.order, g.AddSymbol(symbol))
	}

	return result
}

// qualifiers finds the names declared in more than one namespace within one
// module, which then carry their file stem.
func qualifiers(states []*fileState) map[string]map[string]bool {
	namespaces := map[string]map[string]map[string]bool{}
	for _, state := range states {
		for _, declaration := range state.declarations {
			if declaration.owner != nil {
				continue
			}

			module := owningModule(states, state, declaration)
			if namespaces[module] == nil {
				namespaces[module] = map[string]map[string]bool{}
			}

			if namespaces[module][declaration.name] == nil {
				namespaces[module][declaration.name] = map[string]bool{}
			}

			namespaces[module][declaration.name][declaration.namespace] = true
		}
	}

	result := map[string]map[string]bool{}
	for module, names := range namespaces {
		for name, seen := range names {
			if len(seen) > 1 {
				if result[module] == nil {
					result[module] = map[string]bool{}
				}

				result[module][name] = true
			}
		}
	}

	return result
}

// qualifiedName gives a declaration's symbol name and its parent's, with
// the file stem in front when the top-level name is not unique in its
// module. The stem's dots become underscores, so a name keeps dots only
// between a type and its members.
func qualifiedName(declaration *declaration, state *fileState, qualified map[string]map[string]bool) (name, parent string) {
	top := declaration
	for top.owner != nil {
		top = top.owner
	}

	prefix := ""
	if qualified[state.module][top.name] {
		prefix = strings.ReplaceAll(strings.TrimSuffix(path.Base(state.path), ".cs"), ".", "_") + "/"
	}

	if declaration.owner != nil {
		parent = prefix + declaration.owner.name
	}

	return prefix + declaration.name, parent
}

// hashText is what a declaration's hash covers: its signature, its body,
// and for a type the bodies that belong to no member.
func hashText(declaration *declaration) string {
	var parts []string
	parts = append(parts, declaration.signature)
	for _, each := range declaration.body {
		parts = append(parts, each.text)
	}

	for _, each := range declaration.hidden {
		parts = append(parts, each.text)
	}

	return strings.Join(parts, " ")
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:6])
}

// Skipped lists the files the last Extract could not read: C# files that
// do not parse and project files that are not XML.
//
// Returns:
//   - result: each file with the reason.
func (this *Extractor) Skipped() []skip {
	return this.skipped
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

// owningModule gives the module a declaration belongs to: its own file's,
// or for a part of a partial type, the module of the type's first part by
// file path.
func owningModule(states []*fileState, state *fileState, declaration *declaration) string {
	top := declaration
	for top.owner != nil {
		top = top.owner
	}

	if !top.partial {
		return state.module
	}

	first := state
	for _, other := range states {
		if other.path >= first.path || other.project != state.project {
			continue
		}

		for _, candidate := range other.declarations {
			if candidate.owner == nil && candidate.partial && candidate.name == top.name && candidate.namespace == top.namespace {
				first = other
			}
		}
	}

	return first.module
}

// generatedName reports whether a file name marks generated code.
func generatedName(file string) bool {
	name := path.Base(file)
	for _, suffix := range []string{".g.cs", ".g.i.cs", ".generated.cs", ".Designer.cs"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}

	return strings.HasPrefix(name, "TemporaryGeneratedFile_")
}

// generatedText reports whether a file's leading comments mark generated
// code.
func generatedText(src []byte) bool {
	for _, line := range strings.Split(strings.TrimPrefix(string(src), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*"):
			if strings.Contains(line, "<auto-generated") || strings.Contains(line, "<autogenerated") {
				return true
			}
		default:
			return false
		}
	}

	return false
}

// skippedDir reports whether a directory holds build output, dependencies
// or test data rather than source.
func skippedDir(name string) bool {
	switch name {
	case "bin", "obj", "testdata", "vendor", "node_modules":
		return true
	}

	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// walk lists every C# source file and project file under root.
func walk(root string) (result tree, err error) {
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

			return nil
		}

		switch {
		case strings.HasSuffix(entry.Name(), ".cs"):
			result.sources = append(result.sources, relative)
		case strings.HasSuffix(entry.Name(), ".csproj") || entry.Name() == propsName:
			result.projects = append(result.projects, relative)
		}

		return nil
	})

	sort.Strings(result.sources)
	sort.Strings(result.projects)
	return result, err
}
