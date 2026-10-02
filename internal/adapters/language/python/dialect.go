package python

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrNotDeclaration = errors.New("not a declaration")

// Dialect reads Python declaration lines in AutoPen, with the same parser
// the extractor uses.
type Dialect struct{}

// NewDialect creates the Python dialect.
//
// Returns:
//   - result: the dialect.
func NewDialect() *Dialect {
	return &Dialect{}
}

// Compatible applies Python's breaking-change rules: parameter names may
// change, fields may be added and variable values may change.
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

// Declare classifies one Python declaration line.
//
// Parameters:
//   - text: the declaration, such as def resolve(name: str) -> Thing.
//   - parent: the enclosing class, or nil at the top level.
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
	for _, declaration := range parseFile(src).declarations {
		if declaration.owner == nil {
			top = append(top, declaration)
		}
	}

	if len(top) != 1 {
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	declaration := top[0]
	return app.Declaration{Kind: declaration.kind, Name: declaration.name, Contract: contract(declaration), Key: declaration.key}, nil
}

// IsFile reports whether a header path names a Python file.
//
// Parameters:
//   - name: the header path.
//
// Returns:
//   - result: true for .py files.
func (this *Dialect) IsFile(name string) bool {
	return strings.HasSuffix(name, ".py")
}

// Language is the id prefix for Python.
//
// Returns:
//   - result: "py".
func (this *Dialect) Language() string {
	return Language
}

// ModulePath derives the package directory from a header path.
//
// Parameters:
//   - name: a file or directory path.
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
		parent = &app.Declaration{Kind: graph.KindType, Name: "_", Contract: true}
	}

	declaration, err := this.Declare(symbol.Signature, parent)
	if err != nil {
		return symbol.Signature
	}

	return declaration.Key
}

// member classifies one method or attribute line of a class.
func member(src []byte, parent *app.Declaration) (result app.Declaration, err error) {
	reader := &parser{src: src, lines: logicalLines(lex(src)), file: &sourceFile{src: src}}
	if len(reader.lines) != 1 {
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, string(src))
	}

	tokens := reader.lines[0].tokens
	if tokens[0].is("def") || tokens[0].is("async") {
		owner := &declaration{kind: parent.Kind, name: parent.Name}
		method := reader.function(0, 1, owner)
		if method == nil {
			return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, string(src))
		}

		name := strings.TrimPrefix(method.name, owner.name+".")
		return app.Declaration{Kind: graph.KindMethod, Name: name, Contract: parent.Contract && public(name), Key: method.key}, nil
	}

	if tokens[0].kind != tokenIdent || isKeyword(tokens[0].text) && !tokens[0].is("self") || len(tokens) > 1 && !(tokens[1].is(":") || tokens[1].is("=") || tokens[1].is(".")) {
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, string(src))
	}

	name := fieldName(string(src))
	return app.Declaration{Kind: app.KindField, Name: name, Contract: parent.Contract && public(name), Key: collapse(string(src))}, nil
}
