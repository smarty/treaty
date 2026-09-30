package golang

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

var (
	qualifier  = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.`)
	whitespace = regexp.MustCompile(`\s+`)
)

// declaration is one top-level Go declaration, or one interface method.
type declaration struct {
	kind      string
	name      string
	parent    string
	receiver  string
	pointer   bool
	signature string
	fields    []graph.Field
	embedded  [][]token
	line      int
	endLine   int
	hashText  string
	refs      []token
}

// funcSignature is the parsed shape of a function or method signature.
type funcSignature struct {
	name       string
	typeParams []token
	params     []token
	result     []token
}

// importSpec is one import, with its explicit alias when there is one.
type importSpec struct {
	alias string
	path  string
	line  int
}

// sourceFile is everything the extractor needs from one Go file.
type sourceFile struct {
	src          []byte
	pkg          string
	imports      []importSpec
	declarations []*declaration
}

// parser walks the tokens of one file.
type parser struct {
	src    []byte
	tokens []token
	pos    int
}

// parseFile reads the contract-level structure of one Go file.
func parseFile(src []byte) *sourceFile {
	this := &parser{src: src, tokens: lex(src)}
	result := &sourceFile{src: src}
	for this.pos < len(this.tokens) {
		current := this.tokens[this.pos]
		switch {
		case current.is("package") && this.pos+1 < len(this.tokens):
			result.pkg = this.tokens[this.pos+1].text
			this.pos += 2
		case current.is("import"):
			result.imports = append(result.imports, this.imports()...)
		case current.is("func"):
			if declaration := this.function(); declaration != nil {
				result.declarations = append(result.declarations, declaration)
			}
		case current.is("type"):
			result.declarations = append(result.declarations, this.group(this.typeSpec)...)
		case current.is("const") || current.is("var"):
			keyword := current.text
			result.declarations = append(result.declarations, this.group(func(spec []token) []*declaration { return this.valueSpec(keyword, spec) })...)
		default:
			this.pos++
		}
	}

	return result
}

func (this *parser) at(offset int) token {
	if this.pos+offset < len(this.tokens) {
		return this.tokens[this.pos+offset]
	}

	return token{kind: tokenSemicolon, text: ";"}
}

func (this *parser) function() *declaration {
	start := this.pos
	this.pos++
	var receiver []token
	if this.at(0).is("(") {
		end := matching(this.tokens, this.pos)
		receiver = this.tokens[this.pos+1 : end]
		this.pos = end + 1
	}

	signature, next := readFuncSignature(this.tokens, this.pos)
	if signature.name == "" {
		this.pos = next
		return nil
	}

	this.pos = next
	var body []token
	if this.at(0).is("{") {
		end := matching(this.tokens, this.pos)
		body = this.tokens[this.pos : end+1]
		this.pos = end + 1
	}

	whole := this.tokens[start:this.pos]
	result := &declaration{
		kind:      graph.KindFunction,
		name:      signature.name,
		signature: signature.text(this.src),
		line:      this.tokens[start].line,
		endLine:   whole[len(whole)-1].line,
		hashText:  spanText(this.src, whole),
		refs:      concat(signature.typeParams, signature.params, signature.result, body),
	}

	if receiver == nil {
		return result
	}

	name, typeName, pointer := readReceiver(receiver)
	if typeName == "" {
		return nil
	}

	result.kind, result.parent, result.receiver, result.pointer = graph.KindMethod, typeName, name, pointer
	result.name = typeName + "." + signature.name
	return result
}

func (this *parser) group(spec func(tokens []token) []*declaration) []*declaration {
	this.pos++
	var result []*declaration
	if !this.at(0).is("(") {
		end := untilDepthZero(this.tokens, this.pos, ";")
		result = spec(this.tokens[this.pos:end])
		this.pos = end + 1
		return result
	}

	close := matching(this.tokens, this.pos)
	this.pos++
	for this.pos < close {
		end := min(untilDepthZero(this.tokens, this.pos, ";"), close)
		if end > this.pos {
			result = append(result, spec(this.tokens[this.pos:end])...)
		}

		this.pos = end + 1
	}

	this.pos = close + 1
	return result
}

func (this *parser) imports() []importSpec {
	var result []importSpec
	for _, spec := range this.group(func(spec []token) []*declaration {
		if len(spec) == 0 {
			return nil
		}

		path, _ := strconv.Unquote(spec[len(spec)-1].text)
		alias := ""
		if len(spec) > 1 {
			alias = spec[0].text
		}

		result = append(result, importSpec{alias: alias, path: path, line: spec[len(spec)-1].line})
		return nil
	}) {
		_ = spec
	}

	return result
}

func (this *parser) typeSpec(spec []token) []*declaration {
	if len(spec) == 0 || spec[0].kind != tokenIdent {
		return nil
	}

	name := spec[0].text
	rest := spec[1:]
	var typeParams []token
	if len(rest) > 0 && rest[0].is("[") && isTypeParams(rest) {
		end := matching(rest, 0)
		typeParams, rest = rest[:end+1], rest[end+1:]
	}

	header := "type " + name + spanText(this.src, typeParams)
	result := &declaration{
		kind:     graph.KindType,
		name:     name,
		line:     spec[0].line,
		endLine:  spec[len(spec)-1].line,
		hashText: spanText(this.src, spec),
		refs:     concat(typeParams, rest),
	}

	switch {
	case len(rest) > 0 && rest[0].is("="):
		result.signature = collapse(header + " = " + spanText(this.src, rest[1:]))
	case len(rest) > 1 && rest[0].is("struct") && rest[1].is("{"):
		result.signature = header + " struct"
		result.refs = typeParams
		for _, field := range splitDepthZero(rest[2:matching(rest, 1)], ";") {
			text := collapse(spanText(this.src, field))
			result.fields = append(result.fields, graph.Field{Text: text, Contract: fieldContract(text)})
			if embedded := embeddedType(field); embedded != nil {
				result.embedded = append(result.embedded, embedded)
			}

			result.refs = append(result.refs, fieldType(field)...)
		}
	case len(rest) > 1 && rest[0].is("interface") && rest[1].is("{"):
		result.kind = graph.KindInterface
		result.signature = header + " interface"
		methods := []*declaration{result}
		for _, element := range splitDepthZero(rest[2:matching(rest, 1)], ";") {
			if len(element) > 1 && element[0].kind == tokenIdent && element[1].is("(") {
				methods = append(methods, &declaration{
					kind:      graph.KindMethod,
					name:      name + "." + element[0].text,
					parent:    name,
					signature: collapse("func " + spanText(this.src, element)),
					line:      element[0].line,
					endLine:   element[len(element)-1].line,
					hashText:  spanText(this.src, element),
					refs:      element[1:],
				})
				continue
			}

			text := collapse(spanText(this.src, element))
			result.fields = append(result.fields, graph.Field{Text: text, Contract: fieldContract(text)})
		}

		return methods
	default:
		result.signature = collapse(header + " " + spanText(this.src, rest))
	}

	return []*declaration{result}
}

func (this *parser) valueSpec(keyword string, spec []token) []*declaration {
	var names []token
	index := 0
	for index < len(spec) && spec[index].kind == tokenIdent {
		names = append(names, spec[index])
		index++
		if index < len(spec) && spec[index].is(",") {
			index++
			continue
		}

		break
	}

	typeEnd := untilDepthZero(spec, index, "=")
	typeTokens := spec[index:typeEnd]
	var values [][]token
	if typeEnd < len(spec) {
		values = splitDepthZero(spec[typeEnd+1:], ",")
	}

	var result []*declaration
	blank := false
	for position, name := range names {
		// Every blank name in one spec shares its references, so one
		// declaration of _ covers them all.
		if name.text == "_" && blank {
			continue
		}

		blank = blank || name.text == "_"

		signature := keyword + " " + name.text
		if len(typeTokens) > 0 {
			signature += " " + spanText(this.src, typeTokens)
		}

		if keyword == "const" && position < len(values) {
			signature += " = " + shortValue(spanText(this.src, values[position]))
		}

		result = append(result, &declaration{
			kind:      graph.KindValue,
			name:      name.text,
			signature: collapse(signature),
			line:      spec[0].line,
			endLine:   spec[len(spec)-1].line,
			hashText:  spanText(this.src, spec),
			refs:      spec[index:],
		})
	}

	return result
}

// text renders a signature the way CML writes it: no receiver, no body.
func (this funcSignature) text(src []byte) string {
	result := "func " + this.name + spanText(src, this.typeParams) + spanText(src, this.params)
	if len(this.result) > 0 {
		result += " " + spanText(src, this.result)
	}

	return collapse(result)
}

// key is the comparison key: name, type parameters, parameter types and
// result types, with parameter names and package qualifiers dropped.
func (this funcSignature) key(src []byte) string {
	result := this.name + stripQualifiers(collapse(spanText(src, this.typeParams)))
	result += "(" + strings.Join(paramTypes(src, this.params), ",") + ")"
	switch {
	case len(this.result) == 0:
	case this.result[0].is("(") && matching(this.result, 0) == len(this.result)-1:
		types := paramTypes(src, this.result)
		if len(types) == 1 {
			result += types[0]
		} else {
			result += "(" + strings.Join(types, ",") + ")"
		}
	default:
		result += stripQualifiers(collapse(spanText(src, this.result)))
	}

	return result
}

func collapse(text string) string {
	text = whitespace.ReplaceAllString(strings.TrimSpace(text), " ")
	text = strings.ReplaceAll(text, "( ", "(")
	text = strings.ReplaceAll(text, " )", ")")
	text = strings.ReplaceAll(text, ",)", ")")
	text = strings.ReplaceAll(text, "{ ", "{")
	text = strings.ReplaceAll(text, " }", "}")
	return text
}

func concat(parts ...[]token) []token {
	var result []token
	for _, part := range parts {
		result = append(result, part...)
	}

	return result
}

// embeddedType returns the type of an embedded struct field, or nil when the
// field is named.
func embeddedType(field []token) []token {
	tokens := field
	if len(tokens) > 0 && tokens[len(tokens)-1].kind == tokenString {
		tokens = tokens[:len(tokens)-1]
	}

	index := 0
	if index < len(tokens) && tokens[index].is("*") {
		index++
	}

	if index >= len(tokens) || tokens[index].kind != tokenIdent {
		return nil
	}

	index++
	if index+1 < len(tokens) && tokens[index].is(".") && tokens[index+1].kind == tokenIdent {
		index += 2
	}

	if index < len(tokens) && tokens[index].is("[") {
		index = matching(tokens, index) + 1
	}

	if index != len(tokens) {
		return nil
	}

	return tokens
}

func exported(name string) bool {
	for _, r := range name {
		return r >= 'A' && r <= 'Z' || (r > 127 && strings.ToUpper(string(r)) == string(r) && strings.ToLower(string(r)) != string(r))
	}

	return false
}

func fieldContract(text string) bool {
	first := strings.TrimLeft(strings.Fields(text + " ")[0], "*")
	if index := strings.LastIndex(first, "."); index >= 0 && !strings.Contains(text, " ") {
		first = first[index+1:]
	}

	return exported(strings.TrimSuffix(first, ","))
}

// fieldType returns the type tokens of a struct field: everything after the
// names, or the whole field when it is embedded. Tags are dropped.
func fieldType(field []token) []token {
	if embedded := embeddedType(field); embedded != nil {
		return embedded
	}

	index := 0
	for index < len(field) && field[index].kind == tokenIdent {
		index++
		if index < len(field) && field[index].is(",") {
			index++
			continue
		}

		break
	}

	end := len(field)
	if end > index && field[end-1].kind == tokenString {
		end--
	}

	return field[index:end]
}

// isTypeParams tells a type parameter list from an array length after a
// type name: [T any] or [K comparable, V any], not [4] or [N+1].
func isTypeParams(tokens []token) bool {
	end := matching(tokens, 0)
	inner := tokens[1:end]
	if len(inner) < 2 || inner[0].kind != tokenIdent {
		return false
	}

	second := inner[1]
	return second.kind == tokenIdent || second.is("*") || second.is("[") || second.is("~") || second.is(",") || second.is("interface") || second.is("func") || second.is("map") || second.is("chan")
}

// matching returns the index of the bracket closing the one at open.
func matching(tokens []token, open int) int {
	depth := 0
	for index := open; index < len(tokens); index++ {
		switch tokens[index].text {
		case "(", "[", "{":
			if tokens[index].kind == tokenOperator {
				depth++
			}
		case ")", "]", "}":
			if tokens[index].kind == tokenOperator {
				depth--
				if depth == 0 {
					return index
				}
			}
		}
	}

	return len(tokens) - 1
}

// paramTypes lists the type of every parameter, repeating a type shared by
// several names, as in (a, b int).
func paramTypes(src []byte, list []token) []string {
	if len(list) < 2 {
		return nil
	}

	segments := splitDepthZero(list[1:matching(list, 0)], ",")
	named := false
	for _, segment := range segments {
		if hasName(segment) {
			named = true
		}
	}

	var result []string
	pending := 0
	for _, segment := range segments {
		if len(segment) == 0 {
			continue
		}

		if !named {
			result = append(result, stripQualifiers(collapse(spanText(src, segment))))
			continue
		}

		if !hasName(segment) {
			pending++
			continue
		}

		typeText := stripQualifiers(collapse(spanText(src, segment[1:])))
		for range pending + 1 {
			result = append(result, typeText)
		}

		pending = 0
	}

	return result
}

// hasName reports whether a parameter segment starts with a name followed by
// a type, as in "ctx context.Context", rather than being a bare type.
func hasName(segment []token) bool {
	if len(segment) < 2 || segment[0].kind != tokenIdent || isKeyword(segment[0].text) {
		return false
	}

	second := segment[1]
	switch {
	case second.is("."):
		return false
	case second.is("["):
		end := matching(segment, 1)
		return end == 2 || end < len(segment)-1
	default:
		return true
	}
}

// readFuncSignature reads name, type parameters, parameters and result from
// the tokens after "func" (and after the receiver, for methods).
func readFuncSignature(tokens []token, pos int) (result funcSignature, next int) {
	if pos >= len(tokens) || tokens[pos].kind != tokenIdent {
		return result, pos + 1
	}

	result.name = tokens[pos].text
	pos++
	if pos < len(tokens) && tokens[pos].is("[") {
		end := matching(tokens, pos)
		result.typeParams = tokens[pos : end+1]
		pos = end + 1
	}

	if pos >= len(tokens) || !tokens[pos].is("(") {
		return funcSignature{}, pos
	}

	end := matching(tokens, pos)
	result.params = tokens[pos : end+1]
	pos = end + 1
	start := pos
	for pos < len(tokens) && !tokens[pos].is("{") && tokens[pos].kind != tokenSemicolon {
		if (tokens[pos].is("struct") || tokens[pos].is("interface")) && pos+1 < len(tokens) && tokens[pos+1].is("{") {
			pos = matching(tokens, pos+1) + 1
			continue
		}

		if tokens[pos].is("(") || tokens[pos].is("[") {
			pos = matching(tokens, pos) + 1
			continue
		}

		pos++
	}

	result.result = tokens[start:pos]
	return result, pos
}

// readReceiver reads the receiver variable name, base type name and whether
// the receiver is a pointer.
func readReceiver(tokens []token) (name string, typeName string, pointer bool) {
	if len(tokens) >= 2 && tokens[0].kind == tokenIdent && !tokens[1].is(".") && !tokens[1].is("[") {
		name, tokens = tokens[0].text, tokens[1:]
	}

	if len(tokens) > 0 && tokens[0].is("*") {
		pointer, tokens = true, tokens[1:]
	}

	if len(tokens) > 0 && tokens[0].kind == tokenIdent {
		typeName = tokens[0].text
	}

	return name, typeName, pointer
}

func shortValue(text string) string {
	if len(text) > 60 || strings.ContainsAny(text, "\n\r") {
		return "…"
	}

	return text
}

// spanText renders tokens as source text with comments removed and every
// gap reduced to one space.
func spanText(src []byte, tokens []token) string {
	var builder strings.Builder
	previous := -1
	for _, current := range tokens {
		if current.auto {
			continue
		}

		if previous >= 0 && current.start > previous {
			builder.WriteByte(' ')
		}

		builder.Write(src[current.start:current.end])
		previous = current.end
	}

	return builder.String()
}

// splitDepthZero splits tokens at a separator that is not nested in brackets.
func splitDepthZero(tokens []token, separator string) [][]token {
	var result [][]token
	start := 0
	for index := 0; index < len(tokens); {
		switch {
		case tokens[index].kind == tokenOperator && (tokens[index].text == "(" || tokens[index].text == "[" || tokens[index].text == "{"):
			index = matching(tokens, index) + 1
		case tokens[index].text == separator && (tokens[index].kind == tokenOperator || tokens[index].kind == tokenSemicolon):
			result = append(result, tokens[start:index])
			index++
			start = index
		default:
			index++
		}
	}

	if start < len(tokens) {
		result = append(result, tokens[start:])
	}

	return result
}

func stripQualifiers(text string) string {
	return qualifier.ReplaceAllString(text, "")
}

// untilDepthZero returns the index of the first separator at bracket depth
// zero, or len(tokens).
func untilDepthZero(tokens []token, pos int, separator string) int {
	for pos < len(tokens) {
		current := tokens[pos]
		if current.kind == tokenOperator && (current.text == "(" || current.text == "[" || current.text == "{") {
			pos = matching(tokens, pos) + 1
			continue
		}

		if current.text == separator && (current.kind == tokenOperator || current.kind == tokenSemicolon) {
			return pos
		}

		pos++
	}

	return pos
}
