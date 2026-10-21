package csharp

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
)

// Language is the id prefix for C#.
const Language = "cs"

var ErrNotDeclaration = errors.New("not a declaration")

// visibilityRank orders the access words from narrow to wide, for the
// rule that an accessor or a type may grow its visibility.
var visibilityRank = map[string]int{
	"private": 0, "file": 0, "private protected": 1, "protected private": 1,
	"internal": 2, "protected": 2, "protected internal": 3, "internal protected": 3, "public": 4,
}

// Dialect reads C# declaration lines in AutoPen, with the same parser the
// extractor uses.
type Dialect struct{}

// NewDialect creates the C# dialect.
//
// Returns:
//   - result: the dialect.
func NewDialect() *Dialect {
	return &Dialect{}
}

// Compatible applies C#'s breaking-change rules: parameter names and
// method type parameter names may change, a property may gain accessors, a
// field's initializer may change, and a type may gain bases or lose
// abstract or sealed. A const or enum value, or a default value, may not
// change, since callers keep a copy.
//
// Notes:
//   - Compatible does not see the parent type, so it cannot tell a type
//     parameter of the parent from a type: renaming one is breaking on the
//     members that use it.
//
// Parameters:
//   - before: the symbol in the base graph.
//   - after: the symbol in the head graph.
//
// Returns:
//   - result: true when existing callers keep working.
func (this *Dialect) Compatible(before, after *graph.Symbol) bool {
	old, errOld := parseSymbol(before)
	current, errCurrent := parseSymbol(after)
	if errOld != nil || errCurrent != nil {
		return collapse(before.Signature) == collapse(after.Signature)
	}

	if old.member != current.member {
		return false
	}

	switch current.member {
	case memberProperty, memberIndexer:
		return propertyCompatible(old, current)
	case memberConst, memberEnumValue:
		return collapse(old.signature) == collapse(current.signature)
	case memberInterface:
		return interfaceKey(old) == interfaceKey(current)
	case memberClass, memberStruct, memberRecord, "record struct", memberEnum, memberDelegate:
		return typeCompatible(old, current)
	default:
		return compareKey(old) == compareKey(current)
	}
}

// Declare classifies one C# declaration line.
//
// Parameters:
//   - text: the declaration, such as public void Add(Line line, int count).
//   - parent: the enclosing type, or nil at the top level.
//
// Returns:
//   - result: the kind, name, contract flag and comparison key.
//   - err: the line is not a declaration.
//
// Errors:
//   - ErrNotDeclaration: the line matches no declaration form.
func (this *Dialect) Declare(text string, parent *app.Declaration) (result app.Declaration, err error) {
	text = strings.TrimSpace(text)
	if parent == nil {
		if text == programName {
			return app.Declaration{Kind: graph.KindFunction, Name: programName, Key: programName}, nil
		}

		declared, err := topLevel(text)
		if err != nil {
			return app.Declaration{}, err
		}

		return app.Declaration{Kind: declared.kind, Name: declared.name, Contract: declared.directory, Key: lineKey(declared)}, nil
	}

	if parent.Kind != graph.KindType && parent.Kind != graph.KindInterface {
		return app.Declaration{}, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	declared, err := memberLine(text, keyPart(parent.Key, "kind"), parent.Name, splitList(keyPart(parent.Key, "tp")))
	if err != nil {
		return app.Declaration{}, err
	}

	name := strings.TrimPrefix(declared.name, declared.owner.name+".")
	key := compareKey(declared)
	if declared.kind == graph.KindType || declared.kind == graph.KindInterface {
		key = lineKey(declared)
	}

	return app.Declaration{Kind: declared.kind, Name: name, Contract: parent.Contract && declared.directory, Key: key}, nil
}

// IsFile reports whether a header path names a C# file.
//
// Parameters:
//   - name: the header path.
//
// Returns:
//   - result: true for .cs files.
func (this *Dialect) IsFile(name string) bool {
	return strings.HasSuffix(name, ".cs")
}

// Language is the id prefix for C#.
//
// Returns:
//   - result: "cs".
func (this *Dialect) Language() string {
	return Language
}

// ModulePath derives the module directory from a header path.
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

// topLevel reads one type declaration line.
func topLevel(text string) (*declaration, error) {
	file, err := parseFile([]byte(text))
	if err != nil || len(file.declarations) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	declared := file.declarations[0]
	if declared.owner != nil || (declared.kind != graph.KindType && declared.kind != graph.KindInterface) || len(file.declarations) > 1 && file.declarations[1].owner == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	return declared, nil
}

// memberLine reads one member line inside a stand-in for its parent type:
// an enum, an interface or a class with the parent's simple name and the
// type parameters in scope.
func memberLine(text, kind, parent string, typeParams []string) (*declaration, error) {
	simple := strings.TrimRight(simpleName(parent), "<,>")
	if cut := strings.LastIndex(simple, "/"); cut >= 0 {
		simple = simple[cut+1:]
	}

	if simple == "" || strings.ContainsAny(simple, "<>") {
		return nil, fmt.Errorf("%w: parent %q", ErrNotDeclaration, parent)
	}

	body := text
	if !strings.HasSuffix(body, ";") {
		body += ";"
	}

	wrapper := "class"
	switch kind {
	case memberInterface:
		wrapper = "interface"
	case memberEnum:
		wrapper = "enum"
		body = strings.TrimSuffix(strings.TrimPrefix(text, "public "), ";")
	}

	generic := ""
	if len(typeParams) > 0 && kind != memberEnum {
		generic = "<" + strings.Join(typeParams, ",") + ">"
	}

	src := wrapper + " " + simple + generic + " {\n" + body + "\n}"
	file, err := parseFile([]byte(src))
	if err != nil || len(file.declarations) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	owner := file.declarations[0]
	var members []*declaration
	for _, each := range file.declarations[1:] {
		if each.owner == owner {
			members = append(members, each)
		}
	}

	if len(members) != 1 || owner.owner != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotDeclaration, text)
	}

	return members[0], nil
}

// parseSymbol reads a symbol's signature, as a type line or as a member
// of a stand-in type with the symbol's parent name and no type parameters.
func parseSymbol(symbol *graph.Symbol) (*declaration, error) {
	if symbol.Kind == graph.KindType || symbol.Kind == graph.KindInterface {
		if symbol.Parent == "" {
			return topLevel(symbol.Signature)
		}
	}

	kind := memberClass
	if enumMember(symbol.Signature) {
		kind = memberEnum
	}

	parent := symbol.Parent
	if parent == "" {
		parent = "_"
	}

	return memberLine(symbol.Signature, kind, parent, nil)
}

// enumMember reports whether a signature is an enum member: an access word
// and a name, with or without a value.
func enumMember(signature string) bool {
	tokens, err := lex([]byte(signature))
	if err != nil {
		return false
	}

	if eq := slices.IndexFunc(tokens, func(each token) bool { return each.is("=") }); eq >= 0 {
		tokens = tokens[:eq]
	}

	return len(tokens) == 2 && tokens[0].is("public") && tokens[1].kind == tokenIdent
}

// lineKey is the key of a type line: the type kind, the type parameters in
// scope, then the compare key.
func lineKey(declared *declaration) string {
	return "kind=" + declared.member + ";tp=" + strings.Join(declared.scope, ",") + ";" + typeKey(declared)
}

// keyPart reads one name=value; part from the start of a type line's key.
func keyPart(key, name string) string {
	for _, part := range strings.Split(key, ";") {
		if value, found := strings.CutPrefix(part, name+"="); found {
			return value
		}
	}

	return ""
}

func splitList(text string) []string {
	if text == "" {
		return nil
	}

	return strings.Split(text, ",")
}

// visibility gives a declaration's access words, or the default the
// signature prints.
func visibility(declared *declaration) string {
	if words := accessOf(declared.modifiers); len(words) > 0 {
		return strings.Join(words, " ")
	}

	switch {
	case declared.member == memberEnumValue:
		return "public"
	case declared.owner == nil:
		return "internal"
	case declared.owner.member == memberInterface:
		return "public"
	default:
		return "private"
	}
}

// flags keeps the modifiers that a compare key holds, in a fixed order.
func flags(declared *declaration, names ...string) string {
	var result []string
	for _, name := range names {
		if slices.Contains(declared.modifiers, name) {
			result = append(result, name)
		}
	}

	return strings.Join(result, " ")
}

// compareKey is what callers depend on in a member: visibility, the
// modifiers that change calls, the type, and the parameter types,
// modifiers and default values. Parameter names are left out.
func compareKey(declared *declaration) string {
	scope := declared.scope
	if declared.owner != nil {
		scope = declared.owner.scope
	}

	typeText := fullType(declared.typeTokens, scope, declared.typeParams)
	switch declared.member {
	case memberMethod, memberConstructor, memberOperator:
		var parameters []string
		for _, each := range declared.parameters {
			parameters = append(parameters, strings.TrimSpace(each.modifier+" "+each.full)+"="+each.value)
		}

		return strings.Join([]string{declared.member, visibility(declared), flags(declared, "static", "abstract", "virtual", "sealed"), typeText,
			strconv.Itoa(len(declared.typeParams)), strings.Join(parameters, ",")}, "|")
	case memberField:
		return strings.Join([]string{declared.member, visibility(declared), flags(declared, "static", "readonly", "const", "required", "volatile"), typeText}, "|")
	case memberEvent:
		return strings.Join([]string{declared.member, visibility(declared), flags(declared, "static", "abstract", "virtual"), typeText}, "|")
	case memberProperty, memberIndexer:
		var parameters []string
		for _, each := range declared.parameters {
			parameters = append(parameters, each.full)
		}

		return strings.Join([]string{declared.member, visibility(declared), flags(declared, "static", "required"), typeText, strings.Join(parameters, ","), strings.Join(declared.accessors, ",")}, "|")
	case memberConst, memberEnumValue:
		return collapse(declared.signature)
	default:
		return typeKey(declared)
	}
}

// typeKey is what callers depend on in a type: its kind, visibility,
// static, abstract and sealed, its bases, its type parameter count and
// constraints. A delegate adds its signature types.
func typeKey(declared *declaration) string {
	parts := []string{declared.member, visibility(declared), flags(declared, "static", "abstract", "sealed"), strconv.Itoa(len(declared.typeParams)),
		strings.Join(baseKeys(declared), ","), constraints(declared)}
	if declared.member == memberDelegate {
		for _, each := range declared.parameters {
			parts = append(parts, strings.TrimSpace(each.modifier+" "+each.full))
		}
	}

	return strings.Join(parts, "|")
}

// baseKeys writes a type's bases with positional type parameters, sorted.
func baseKeys(declared *declaration) []string {
	var result []string
	for _, base := range declared.bases {
		result = append(result, fullType(base, declared.scope, nil))
	}

	slices.Sort(result)
	return result
}

// constraints writes a type's where clauses with positional type
// parameters.
func constraints(declared *declaration) string {
	tokens, err := lex([]byte(declared.where))
	if err != nil {
		return declared.where
	}

	var result []string
	for _, each := range tokens {
		text := each.text
		if at := lastIndex(declared.scope, text); at >= 0 && each.kind == tokenIdent {
			text = "T" + strconv.Itoa(at)
		}

		result = append(result, text)
	}

	return strings.Join(result, " ")
}

// typeCompatible applies the type rules: the same kind, static flag,
// type parameter count, constraints and visibility or wider; abstract and
// sealed may only go; bases may only grow. An enum keeps its underlying
// type and a delegate its signature.
func typeCompatible(old, current *declaration) bool {
	if flags(old, "static") != flags(current, "static") || len(old.typeParams) != len(current.typeParams) || constraints(old) != constraints(current) {
		return false
	}

	if visibilityRank[visibility(current)] < visibilityRank[visibility(old)] {
		return false
	}

	for _, word := range []string{"abstract", "sealed"} {
		if slices.Contains(current.modifiers, word) && !slices.Contains(old.modifiers, word) {
			return false
		}
	}

	oldBases, currentBases := baseKeys(old), baseKeys(current)
	if old.member == memberEnum {
		return slices.Equal(oldBases, currentBases)
	}

	for _, base := range oldBases {
		if !slices.Contains(currentBases, base) {
			return false
		}
	}

	if old.member == memberDelegate {
		return typeKey(old) == typeKey(current) && fullType(old.typeTokens, old.scope, nil) == fullType(current.typeTokens, current.scope, nil)
	}

	return true
}

// interfaceKey is what implementers depend on in an interface: a new base
// adds members they must supply, so the bases must stay.
func interfaceKey(declared *declaration) string {
	return strings.Join([]string{visibility(declared), strconv.Itoa(len(declared.typeParams)), strings.Join(baseKeys(declared), ","), constraints(declared)}, "|")
}

// propertyCompatible applies the property rules: the same visibility,
// static flag, type and parameters; required may only go; and every
// accessor stays, with the same or wider visibility.
func propertyCompatible(old, current *declaration) bool {
	scope := func(declared *declaration) []string { return declared.owner.scope }
	if visibility(old) != visibility(current) || flags(old, "static") != flags(current, "static") ||
		fullType(old.typeTokens, scope(old), nil) != fullType(current.typeTokens, scope(current), nil) {
		return false
	}

	if len(old.parameters) != len(current.parameters) {
		return false
	}

	for index := range old.parameters {
		if old.parameters[index].full != current.parameters[index].full {
			return false
		}
	}

	if slices.Contains(current.modifiers, "required") && !slices.Contains(old.modifiers, "required") {
		return false
	}

	for _, accessor := range old.accessors {
		keyword, rank := accessorOf(accessor, visibility(old))
		found := false
		for _, other := range current.accessors {
			if otherKeyword, otherRank := accessorOf(other, visibility(current)); otherKeyword == keyword && otherRank >= rank {
				found = true
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// accessorOf splits an accessor such as private set into its keyword and
// the rank of its visibility, which defaults to the property's.
func accessorOf(accessor, property string) (keyword string, rank int) {
	words := strings.Fields(accessor)
	if len(words) == 0 {
		return "", 0
	}

	keyword = words[len(words)-1]
	access := strings.Join(accessOf(words[:len(words)-1]), " ")
	if access == "" {
		access = property
	}

	return keyword, visibilityRank[access]
}

// collapse turns runs of whitespace into single spaces.
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
