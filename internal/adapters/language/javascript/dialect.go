package javascript

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrNotDeclaration = errors.New("not a declaration")

// Dialect reads JavaScript or TypeScript declaration lines in AutoPen, with
// the same parser the extractor uses. The two languages share one dialect
// and differ only in their id prefix.
type Dialect struct {
	language string
}

// NewDialect creates the dialect for one of the two languages.
//
// Parameters:
//   - language: LanguageJavaScript or LanguageTypeScript.
//
// Returns:
//   - result: the dialect.
func NewDialect(language string) *Dialect {
	return &Dialect{language: language}
}

// Compatible applies JavaScript's breaking-change rules: parameter names
// may change, fields may be added and constant values may change.
//
// Parameters:
//   - before: the symbol in the base graph.
//   - after: the symbol in the head graph.
//
// Returns:
//   - result: true when existing callers keep working.
func (this *Dialect) Compatible(before, after *graph.Symbol) bool {
	switch after.Kind {
	case graph.KindFunction, graph.KindMethod:
		return this.key(before) == this.key(after)
	case graph.KindValue:
		return valueKey(before.Signature) == valueKey(after.Signature)
	default:
		return rules.DefaultCompatible(before, after)
	}
}

// Declare classifies one JavaScript or TypeScript declaration line.
//
// Parameters:
//   - text: the declaration, such as export function resolve(name: string): Thing.
//   - parent: the enclosing class or interface, or nil at the top level.
//
// Returns:
//   - result: the kind, name, contract flag and comparison key.
//   - err: the line is not a declaration.
//
// Errors:
//   - ErrNotDeclaration: the line matches no declaration form.
func (this *Dialect) Declare(text string, parent *app.Declaration) (result app.Declaration, err error) {
	src := []byte(collapse(text))
	if parent != nil && (parent.Kind == graph.KindType || parent.Kind == graph.KindInterface) {
		return member(src, parent)
	}

	var top []*declaration
	for _, declaration := range parseFile(src, false).declarations {
		if declaration.owner == nil {
			top = append(top, declaration)
		}
	}

	if len(top) != 1 {
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	declaration := top[0]
	return app.Declaration{Kind: declaration.kind, Name: declaration.name, Contract: declaration.exported, Key: declaration.key}, nil
}

// IsFile reports whether a header path names a JavaScript or TypeScript
// file, or a Vue or Svelte component.
//
// Parameters:
//   - path: the header path.
//
// Returns:
//   - result: true for source files.
func (this *Dialect) IsFile(name string) bool {
	return slices.Contains(sourceExtensions, path.Ext(name)) || component(name)
}

// Language is the id prefix: js or ts.
//
// Returns:
//   - result: the language.
func (this *Dialect) Language() string {
	return this.language
}

// ModulePath derives the module directory from a header path.
//
// Parameters:
//   - path: a file or directory path.
//
// Returns:
//   - result: the directory.
func (this *Dialect) ModulePath(name string) string {
	if !this.IsFile(name) {
		return name
	}

	return path.Dir(name)
}

// key parses a symbol's signature and returns its comparison key, or the
// signature itself when it does not parse.
func (this *Dialect) key(symbol *graph.Symbol) string {
	var parent *app.Declaration
	if symbol.Kind == graph.KindMethod {
		parent = &app.Declaration{Kind: graph.KindType, Contract: true}
	}

	declaration, err := this.Declare(symbol.Signature, parent)
	if err != nil {
		return symbol.Signature
	}

	return declaration.Key
}

// member classifies one class or interface member line.
func member(src []byte, parent *app.Declaration) (result app.Declaration, err error) {
	owner := &declaration{kind: parent.Kind, name: parent.Name, exported: parent.Contract}
	reader := &parser{src: src, tokens: lex(src, false), file: &sourceFile{src: src, exports: map[string]string{}}, bound: map[int]bool{}}
	reader.members(owner, reader.tokens, parent.Kind == graph.KindInterface)
	switch {
	case len(reader.file.declarations) == 1 && len(owner.fields) == 0:
		method := reader.file.declarations[0]
		return app.Declaration{Kind: graph.KindMethod, Name: strings.TrimPrefix(method.name, owner.name+"."), Contract: contract(method), Key: method.key}, nil
	case len(owner.fields) == 1 && len(reader.file.declarations) == 0:
		return app.Declaration{Kind: app.KindField, Name: fieldName(reader.tokens), Contract: parent.Contract && !owner.fieldPrivate[0], Key: owner.fields[0].Text}, nil
	default:
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, string(src))
	}
}

// fieldName finds a member's name after its modifiers.
func fieldName(tokens []token) string {
	k := 0
	for k+1 < len(tokens) && tokens[k].kind == tokenIdent && memberModifier(tokens[k].text) && !nameEnds(tokens[k+1]) {
		k++
	}

	if k < len(tokens) {
		if name, ok := unquote(tokens[k]); ok {
			return name
		}

		return tokens[k].text
	}

	return ""
}

// Satisfies reports false: a class names what it implements, and the
// extractor's implements edges say so already.
//
// Parameters:
//   - g: the graph both symbols are in.
//   - concrete: the concrete type's symbol id.
//   - iface: the interface's symbol id.
//
// Returns:
//   - result: false.
func (this *Dialect) Satisfies(g *graph.Graph, concrete, iface string) bool {
	return false
}

// Alias names the symbol that a name a file re-exports, export { X } from or export * from,
// stands for: the extractor makes one in the file's module, with an edge
// to its target and a hash drawn from the target's id.
//
// Parameters:
//   - g: the graph the symbol is in.
//   - symbol: the symbol's id.
//
// Returns:
//   - result: the id of the symbol it stands for; empty when it is a
//     declaration of its own.
func (this *Dialect) Alias(g *graph.Graph, symbol string) string {
	alias := g.Symbol(symbol)
	if alias == nil {
		return ""
	}

	for _, edge := range g.Edges {
		if edge.From == symbol && alias.Hash == hash("alias "+edge.To) {
			return edge.To
		}
	}

	return ""
}
