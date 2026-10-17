package golang

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var (
	ErrNotDeclaration = errors.New("not a declaration")

	typeName  = regexp.MustCompile(`^type\s+([A-Za-z_][A-Za-z0-9_]*)`)
	valueName = regexp.MustCompile(`^(?:const|var)\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// Dialect reads Go declaration lines in AutoPen, with the same parser the
// extractor uses.
type Dialect struct{}

// NewDialect creates the Go dialect.
//
// Returns:
//   - result: the dialect.
func NewDialect() *Dialect {
	return &Dialect{}
}

// Compatible applies Go's breaking-change rules: parameter names may change,
// struct fields may be added and constant values may change.
//
// Parameters:
//   - before: the symbol in the base graph.
//   - after: the symbol in the head graph.
//
// Returns:
//   - result: true when existing callers keep compiling.
func (this *Dialect) Compatible(before, after *graph.Symbol) bool {
	if before.Pointer != after.Pointer {
		return false
	}

	switch after.Kind {
	case graph.KindFunction, graph.KindMethod:
		return signatureKey(before.Signature) == signatureKey(after.Signature)
	case graph.KindValue:
		beforeHead, _, _ := strings.Cut(before.Signature, " = ")
		afterHead, _, _ := strings.Cut(after.Signature, " = ")
		return beforeHead == afterHead
	default:
		return rules.DefaultCompatible(before, after)
	}
}

// Declare classifies one Go declaration line.
//
// Parameters:
//   - text: the declaration, such as func New() Scope.
//   - parent: the enclosing type, or nil at the top level.
//
// Returns:
//   - result: the kind, name, contract flag and comparison key.
//   - err: the line is not a Go declaration.
//
// Errors:
//   - ErrNotDeclaration: the line matches no Go declaration form.
func (this *Dialect) Declare(text string, parent *app.Declaration) (result app.Declaration, err error) {
	text = collapse(text)
	nested := parent != nil && (parent.Kind == graph.KindType || parent.Kind == graph.KindInterface)
	switch {
	case strings.HasPrefix(text, "func "):
		tokens := lex([]byte(text))
		parsed, next := readFuncSignature(tokens, 1)
		if parsed.name == "" || next < len(tokens) && !tokens[next].auto {
			return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
		}

		result = app.Declaration{Kind: graph.KindFunction, Name: parsed.name, Contract: exported(parsed.name), Key: parsed.key([]byte(text))}
		if nested {
			result.Kind, result.Contract = graph.KindMethod, result.Contract && parent.Contract
		}

		return result, nil
	case nested:
		return app.Declaration{Kind: app.KindField, Name: strings.Fields(text)[0], Contract: fieldContract(text), Key: stripQualifiers(text)}, nil
	case typeName.MatchString(text):
		name := typeName.FindStringSubmatch(text)[1]
		kind := graph.KindType
		if strings.HasSuffix(text, " interface") {
			kind = graph.KindInterface
		}

		return app.Declaration{Kind: kind, Name: name, Contract: exported(name), Key: stripQualifiers(text)}, nil
	case valueName.MatchString(text):
		name := valueName.FindStringSubmatch(text)[1]
		return app.Declaration{Kind: graph.KindValue, Name: name, Contract: exported(name), Key: stripQualifiers(text)}, nil
	default:
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}
}

// IsFile reports whether a header path names a Go file.
//
// Parameters:
//   - path: the header path.
//
// Returns:
//   - result: true for .go files.
func (this *Dialect) IsFile(path string) bool {
	return strings.HasSuffix(path, ".go")
}

// Language is the id prefix for Go.
//
// Returns:
//   - result: "go".
func (this *Dialect) Language() string {
	return "go"
}

// ModulePath derives the package directory from a header path.
//
// Parameters:
//   - path: a file or package path.
//
// Returns:
//   - result: the package directory.
func (this *Dialect) ModulePath(path string) string {
	if !this.IsFile(path) {
		return path
	}

	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[:index]
	}

	return "."
}

// Satisfies reports whether a concrete type's methods cover an interface's,
// the structural match Go's implicit interfaces make, whether or not the
// type's package imports the interface's. An alias of an interface is
// followed to it.
//
// Parameters:
//   - g: the graph both symbols are in.
//   - concrete: the concrete type's symbol id.
//   - iface: the interface's symbol id, or an alias of one.
//
// Returns:
//   - result: true when the type has every method of the interface.
func (this *Dialect) Satisfies(g *graph.Graph, concrete, iface string) bool {
	for range aliasHops {
		target := this.Alias(g, iface)
		if target == "" {
			break
		}

		iface = target
	}

	typed, port := g.Symbol(concrete), g.Symbol(iface)
	if typed == nil || port == nil || typed.Kind != graph.KindType || port.Kind != graph.KindInterface {
		return false
	}

	methods := methodSets(append(g.SymbolsIn(typed.Module), g.SymbolsIn(port.Module)...))
	return covers(methods[typed.ID], methods[port.ID])
}

// Alias names the type a type alias, type X = pkg.Y, stands for, through
// the type use the alias's declaration records.
//
// Parameters:
//   - g: the graph the symbol is in.
//   - symbol: the symbol's id.
//
// Returns:
//   - result: the id of the aliased type; empty when the symbol is not an
//     alias, or aliases a type the graph does not hold.
func (this *Dialect) Alias(g *graph.Graph, symbol string) string {
	alias := g.Symbol(symbol)
	if alias == nil || alias.Kind != graph.KindType || !strings.HasPrefix(alias.Signature, "type "+alias.Name+" = ") {
		return ""
	}

	for _, edge := range g.Edges {
		if edge.From == symbol && edge.Kind == graph.EdgeTypeUse && g.Symbol(edge.To) != nil {
			return edge.To
		}
	}

	return ""
}

// aliasHops caps how many aliases Satisfies follows, so a cycle of them
// cannot loop.
const aliasHops = 8
