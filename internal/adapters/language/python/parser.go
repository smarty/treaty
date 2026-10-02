package python

import (
	"regexp"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

var whitespace = regexp.MustCompile(`\s+`)

// alias is one name a from-import brings in, under the name it binds.
type alias struct {
	name string
	as   string
}

// declaration is one top-level function, class or variable, or one method.
type declaration struct {
	kind         string
	name         string
	owner        *declaration
	signature    string
	key          string
	fields       []graph.Field
	bases        [][]token
	line         int
	endLine      int
	doc          string
	docLine      int
	commentLine  int
	hashText     string
	refs         []token
	initializers []line
}

// importSpec is one import statement's module: import a.b [as c], or from
// .a import b [as c], or from a import *.
type importSpec struct {
	module string
	level  int
	plain  bool
	as     string
	names  []alias
	star   bool
	line   int
}

// line is one logical line: its tokens and its indentation.
type line struct {
	tokens []token
	indent int
}

// parser walks the logical lines of one file, one block at a time.
type parser struct {
	src     []byte
	lines   []line
	file    *sourceFile
	pending []token
	comment int
}

// sourceFile is everything the extractor needs from one file.
type sourceFile struct {
	src          []byte
	imports      []importSpec
	all          []string
	hasAll       bool
	main         bool
	declarations []*declaration
}

// parseFile reads the contract-level structure of one file.
//
// Parameters:
//   - src: the source.
//
// Returns:
//   - result: its imports and declarations.
func parseFile(src []byte) *sourceFile {
	tokens := lex(src)
	this := &parser{src: src, lines: logicalLines(tokens), file: &sourceFile{src: src}}
	this.imports()
	this.block(0, len(this.lines))
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index].is("__name__") && tokens[index+1].is("==") && tokens[index+2].kind == tokenString && strings.Contains(tokens[index+2].text, "__main__") {
			this.file.main = true
		}
	}

	return this.file
}

// assignment reads module variables: name = value, name: type = value, and
// a = b = value or a, b = value with several names.
func (this *parser) assignment(tokens []token) {
	var targets [][]token
	var annotation, value []token
	if len(tokens) > 1 && tokens[0].kind == tokenIdent && tokens[1].is(":") {
		equals := untilTop(tokens, 2, "=")
		annotation = tokens[2:equals]
		targets = [][]token{tokens[:1]}
		if equals < len(tokens) {
			value = tokens[equals+1:]
		}
	} else {
		parts := splitTop(tokens, "=")
		if len(parts) < 2 {
			return
		}

		targets, value = parts[:len(parts)-1], parts[len(parts)-1]
	}

	for _, target := range targets {
		for _, name := range targetNames(target) {
			if name.text == "__all__" {
				this.file.hasAll = true
				for _, current := range value {
					if text, ok := unquote(current); ok {
						this.file.all = append(this.file.all, text)
					}
				}

				continue
			}

			signature := name.text
			if len(annotation) > 0 {
				signature += ": " + spanText(this.src, annotation)
			}

			if len(value) > 0 {
				signature += " = " + shortValue(collapse(spanText(this.src, value)))
			}

			result := &declaration{kind: graph.KindValue, name: name.text, signature: collapse(signature), line: tokens[0].line, endLine: tokens[len(tokens)-1].line, hashText: spanText(this.src, tokens), refs: concat(annotation, value)}
			result.key = valueKey(result.signature)
			this.add(result)
		}
	}
}

// block reads the statements of lines [from, to) at the top level. The
// bodies of if, try, with and other compound statements are read as top
// level too, since they often hold imports and definitions.
func (this *parser) block(from, to int) {
	for index := from; index < to; {
		current := this.lines[index]
		end := min(blockEnd(this.lines, index), to)
		tokens := current.tokens
		first := tokens[0]
		switch {
		case first.is("@"):
			if this.comment == 0 {
				this.comment = first.line
			}

			this.pending = append(this.pending, tokens...)
			index++
			continue
		case first.is("import") || first.is("from"):
		case first.is("def") || first.is("async") && len(tokens) > 1 && tokens[1].is("def"):
			if result := this.function(index, end, nil); result != nil {
				this.add(result)
			}
		case first.is("class"):
			this.class(index, end)
		case compound(tokens):
			this.block(index+1, end)
		case first.is("type") && len(tokens) > 2 && tokens[1].kind == tokenIdent && (tokens[2].is("=") || tokens[2].is("[")):
			result := &declaration{kind: graph.KindType, name: tokens[1].text, signature: collapse(spanText(this.src, tokens)), line: first.line, endLine: tokens[len(tokens)-1].line, hashText: spanText(this.src, tokens), refs: tokens[2:]}
			result.key = result.signature
			this.add(result)
		default:
			this.assignment(tokens)
		}

		this.pending, this.comment = nil, 0
		index = end
	}
}

// class reads a class, its methods, and its attributes: those assigned or
// annotated in the class body and those __init__ assigns to self.
func (this *parser) class(index, end int) {
	tokens := this.lines[index].tokens
	if len(tokens) < 2 || tokens[1].kind != tokenIdent {
		return
	}

	name := tokens[1].text
	colon := untilTop(tokens, 2, ":")
	k := 2
	var typeParams, refs []token
	if k < colon && tokens[k].is("[") {
		close := matching(tokens, k)
		typeParams, k = tokens[k:close+1], close+1
	}

	var bases [][]token
	if k < colon && tokens[k].is("(") {
		close := matching(tokens, k)
		for _, base := range splitTop(tokens[k+1:close], ",") {
			switch {
			case len(base) == 0:
			case untilTop(base, 0, "=") < len(base) || base[0].is("*") || base[0].is("**"):
				refs = append(refs, base...)
			default:
				bases = append(bases, base)
				refs = append(refs, typeArguments(base)...)
			}
		}
	}

	kind := graph.KindType
	for _, base := range bases {
		if baseName(base) == "Protocol" {
			kind = graph.KindInterface
		}
	}

	result := &declaration{
		kind:      kind,
		name:      name,
		signature: collapse(spanText(this.src, tokens[:colon])),
		bases:     bases,
		line:      tokens[0].line,
		endLine:   this.lastLine(index, end),
		hashText:  this.spanLines(index, end),
		refs:      concat(typeParams, refs),
	}

	result.key = result.signature
	this.add(result)
	inline := tokens[min(colon+1, len(tokens)):]
	if len(inline) > 0 {
		result.doc, result.docLine = docstring(inline)
		result.refs = append(result.refs, inline...)
	}

	if index+1 >= end {
		return
	}

	if result.doc == "" {
		result.doc, result.docLine = docstring(this.lines[index+1].tokens)
	}

	indent := this.lines[index+1].indent
	var decorators []token
	comment := 0
	for member := index + 1; member < end; {
		memberEnd := blockEnd(this.lines, member)
		current := this.lines[member]
		first := current.tokens[0]
		switch {
		case current.indent != indent:
		case first.is("@"):
			if comment == 0 {
				comment = first.line
			}

			decorators = append(decorators, current.tokens...)
			member++
			continue
		case first.is("def") || first.is("async") && len(current.tokens) > 1 && current.tokens[1].is("def"):
			if method := this.function(member, memberEnd, result); method != nil {
				method.refs = append(decorators, method.refs...)
				method.commentLine = max(comment, 0)
				if method.commentLine == 0 {
					method.commentLine = method.line
				}

				this.file.declarations = append(this.file.declarations, method)
				if method.name == name+".__init__" {
					this.selfFields(result, method)
				}
			}
		case member == index+1 && first.kind == tokenString:
		default:
			result.refs = append(result.refs, decorators...)
			if field, refs, ok := this.field(current.tokens); ok {
				addField(result, field)
				result.refs = append(result.refs, refs...)
			} else {
				for nested := member; nested < memberEnd; nested++ {
					result.refs = append(result.refs, this.lines[nested].tokens...)
				}
			}
		}

		decorators, comment = nil, 0
		member = memberEnd
	}
}

func (this *parser) add(declaration *declaration) {
	declaration.refs = append(this.pending, declaration.refs...)
	declaration.commentLine = this.comment
	if declaration.commentLine == 0 {
		declaration.commentLine = declaration.line
	}

	this.file.declarations = append(this.file.declarations, declaration)
}

// field reads one class-level attribute line, name: type = value or
// name = value, as a field's text and the tokens it references. Dunder
// attributes such as __slots__ are not fields.
func (this *parser) field(tokens []token) (text string, refs []token, ok bool) {
	switch {
	case len(tokens) > 1 && tokens[0].kind == tokenIdent && tokens[1].is(":"):
		if dunder(tokens[0].text) {
			return "", nil, false
		}

		equals := untilTop(tokens, 2, "=")
		return collapse(spanText(this.src, tokens[:equals])), tokens[2:], true
	case len(tokens) > 2 && tokens[0].kind == tokenIdent && tokens[1].is("="):
		if dunder(tokens[0].text) {
			return "", nil, false
		}

		return tokens[0].text, tokens[2:], true
	default:
		return "", nil, false
	}
}

// function reads a def and its body, as a method when owner is set.
func (this *parser) function(index, end int, owner *declaration) *declaration {
	tokens := this.lines[index].tokens
	k := 0
	async := tokens[0].is("async")
	if async {
		k++
	}

	k++
	if k >= len(tokens) || tokens[k].kind != tokenIdent {
		return nil
	}

	name := tokens[k].text
	k++
	var typeParams, result []token
	if k < len(tokens) && tokens[k].is("[") {
		close := matching(tokens, k)
		typeParams, k = tokens[k:close+1], close+1
	}

	if k >= len(tokens) || !tokens[k].is("(") {
		return nil
	}

	close := matching(tokens, k)
	params := tokens[k : close+1]
	k = close + 1
	if k < len(tokens) && tokens[k].is("->") {
		colon := untilTop(tokens, k+1, ":")
		result, k = tokens[k+1:colon], colon
	}

	declaration := &declaration{
		kind:      graph.KindFunction,
		name:      name,
		owner:     owner,
		signature: collapse(spanText(this.src, tokens[:k])),
		key:       functionKey(this.src, name, async, typeParams, params, result, owner != nil),
		line:      tokens[0].line,
		endLine:   this.lastLine(index, end),
		hashText:  this.spanLines(index, end),
		refs:      concat(typeParams, params, result),
	}

	if owner != nil {
		declaration.kind, declaration.name = graph.KindMethod, owner.name+"."+name
	}

	inline := tokens[min(k+1, len(tokens)):]
	declaration.refs = append(declaration.refs, inline...)
	if len(inline) > 0 {
		declaration.doc, declaration.docLine = docstring(inline)
	} else if index+1 < end {
		declaration.doc, declaration.docLine = docstring(this.lines[index+1].tokens)
	}

	for body := index + 1; body < end; body++ {
		declaration.refs = append(declaration.refs, this.lines[body].tokens...)
	}

	declaration.initializers = this.lines[index+1 : end]
	return declaration
}

// imports reads every import statement in the file, at any depth: an
// import inside a function is a dependency too.
func (this *parser) imports() {
	for _, current := range this.lines {
		tokens := current.tokens
		switch {
		case tokens[0].is("import"):
			for _, part := range splitTop(tokens[1:], ",") {
				module, k := dotted(part, 0)
				if module == "" {
					continue
				}

				spec := importSpec{module: module, plain: true, line: tokens[0].line}
				if k+1 < len(part) && part[k].is("as") {
					spec.as = part[k+1].text
				}

				this.file.imports = append(this.file.imports, spec)
			}
		case tokens[0].is("from"):
			k := 1
			spec := importSpec{line: tokens[0].line}
			for k < len(tokens) && (tokens[k].is(".") || tokens[k].is("...")) {
				spec.level += len(tokens[k].text)
				k++
			}

			spec.module, k = dotted(tokens, k)
			if k >= len(tokens) || !tokens[k].is("import") {
				continue
			}

			names := tokens[k+1:]
			if len(names) > 0 && names[0].is("(") {
				names = names[1:matching(names, 0)]
			}

			for _, part := range splitTop(names, ",") {
				switch {
				case len(part) == 1 && part[0].is("*"):
					spec.star = true
				case len(part) == 1 && part[0].kind == tokenIdent:
					spec.names = append(spec.names, alias{name: part[0].text, as: part[0].text})
				case len(part) == 3 && part[1].is("as"):
					spec.names = append(spec.names, alias{name: part[0].text, as: part[2].text})
				}
			}

			this.file.imports = append(this.file.imports, spec)
		}
	}
}

func (this *parser) lastLine(index, end int) int {
	tokens := this.lines[max(end-1, index)].tokens
	return tokens[len(tokens)-1].line
}

// selfFields adds the attributes __init__ assigns to self, as in
// self.name = value or self.name: type = value.
func (this *parser) selfFields(class, init *declaration) {
	for _, current := range init.initializers {
		tokens := current.tokens
		if len(tokens) < 4 || !tokens[0].is("self") || !tokens[1].is(".") || tokens[2].kind != tokenIdent || !(tokens[3].is("=") || tokens[3].is(":")) {
			continue
		}

		text := tokens[2].text
		if tokens[3].is(":") {
			equals := untilTop(tokens, 4, "=")
			text = collapse(text + ": " + spanText(this.src, tokens[4:equals]))
		}

		addField(class, text)
	}
}

func (this *parser) spanLines(index, end int) string {
	var parts []string
	for current := index; current < end; current++ {
		parts = append(parts, spanText(this.src, this.lines[current].tokens))
	}

	return strings.Join(parts, "\n")
}

// addField adds a field unless the class already has one of that name.
func addField(class *declaration, text string) {
	name := fieldName(text)
	for _, existing := range class.fields {
		if fieldName(existing.Text) == name {
			return
		}
	}

	class.fields = append(class.fields, graph.Field{Text: text})
}

// baseName is the class a base names, without module or type arguments:
// Protocol for typing.Protocol[T].
func baseName(base []token) string {
	name := ""
	for _, current := range base {
		if current.is("[") || current.is("(") {
			break
		}

		if current.kind == tokenIdent {
			name = current.text
		}
	}

	return name
}

// blockEnd returns the index of the first line after line index that is
// not indented deeper than it.
func blockEnd(lines []line, index int) int {
	end := index + 1
	for end < len(lines) && lines[end].indent > lines[index].indent {
		end++
	}

	return end
}

func collapse(text string) string {
	text = whitespace.ReplaceAllString(strings.TrimSpace(text), " ")
	text = strings.ReplaceAll(text, "( ", "(")
	text = strings.ReplaceAll(text, " )", ")")
	text = strings.ReplaceAll(text, "[ ", "[")
	text = strings.ReplaceAll(text, " ]", "]")
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, ",)", ")")
	text = strings.ReplaceAll(text, " :", ":")
	return text
}

// compound reports whether a line opens a compound statement other than
// def and class, such as if, try or with.
func compound(tokens []token) bool {
	first := tokens[0]
	switch first.text {
	case "if", "elif", "else", "try", "except", "finally", "with", "for", "while":
		return first.kind == tokenIdent
	case "async":
		return len(tokens) > 1 && (tokens[1].is("with") || tokens[1].is("for"))
	case "match", "case":
		return len(tokens) > 1 && tokens[len(tokens)-1].is(":") && !tokens[1].is("=") && !tokens[1].is(".")
	}

	return false
}

func concat(parts ...[]token) []token {
	var result []token
	for _, part := range parts {
		result = append(result, part...)
	}

	return result
}

// docstring reads a docstring: a body that opens with a string literal.
// Its lines are dedented as Python's inspect.cleandoc does.
func docstring(tokens []token) (text string, line int) {
	if len(tokens) == 0 || tokens[0].kind != tokenString {
		return "", 0
	}

	raw := tokens[0].text
	raw = strings.TrimLeft(raw, "rRuUbBfF")
	for _, quote := range []string{`"""`, `'''`, `"`, `'`} {
		if strings.HasPrefix(raw, quote) && strings.HasSuffix(raw, quote) && len(raw) >= 2*len(quote) {
			raw = raw[len(quote) : len(raw)-len(quote)]
			break
		}
	}

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	margin := -1
	for _, each := range lines[1:] {
		if trimmed := strings.TrimLeft(each, " \t"); trimmed != "" {
			if indent := len(each) - len(trimmed); margin < 0 || indent < margin {
				margin = indent
			}
		}
	}

	lines[0] = strings.TrimSpace(lines[0])
	for index := 1; index < len(lines); index++ {
		if margin > 0 && len(lines[index]) >= margin {
			lines[index] = lines[index][margin:]
		}

		lines[index] = strings.TrimRight(lines[index], " \t")
	}

	return strings.TrimSpace(strings.Join(lines, "\n")), tokens[0].line
}

// dotted reads a dotted name such as a.b.c starting at k.
func dotted(tokens []token, k int) (name string, next int) {
	var parts []string
	for k < len(tokens) && tokens[k].kind == tokenIdent && !tokens[k].is("import") {
		parts = append(parts, tokens[k].text)
		k++
		if k+1 < len(tokens) && tokens[k].is(".") && tokens[k+1].kind == tokenIdent {
			k++
			continue
		}

		break
	}

	return strings.Join(parts, "."), k
}

// dunder reports whether a name is a special name such as __init__.
func dunder(name string) bool {
	return len(name) > 4 && strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")
}

// fieldName is the attribute a field's text names.
func fieldName(text string) string {
	name, _, _ := strings.Cut(text, ":")
	name, _, _ = strings.Cut(name, "=")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "self."))
}

// functionKey is the comparison key of a function: its name, type
// parameters, parameter types and result, with parameter names dropped.
func functionKey(src []byte, name string, async bool, typeParams, params, result []token, method bool) string {
	key := name + collapse(spanText(src, typeParams)) + "(" + strings.Join(paramKeys(src, params, method), ",") + ")"
	if len(result) > 0 {
		key += "->" + collapse(spanText(src, result))
	}

	if async {
		key = "async " + key
	}

	return key
}

// logicalLines groups tokens into logical lines.
func logicalLines(tokens []token) []line {
	var result []line
	for _, current := range tokens {
		if current.newline || len(result) == 0 {
			result = append(result, line{indent: current.indent})
		}

		result[len(result)-1].tokens = append(result[len(result)-1].tokens, current)
	}

	return result
}

// matching returns the index of the bracket closing the one at open.
func matching(tokens []token, open int) int {
	depth := 0
	for index := open; index < len(tokens); index++ {
		if tokens[index].kind != tokenOperator {
			continue
		}

		switch tokens[index].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return index
			}
		}
	}

	return len(tokens) - 1
}

// paramKeys lists each parameter's comparison key: its annotation, or _
// when it has none, marked * or ** for variadic parameters and ? when it
// has a default. The / and * markers stay, since they decide how callers
// may pass arguments. A method's self or cls is left out.
func paramKeys(src []byte, params []token, method bool) []string {
	if len(params) < 2 {
		return nil
	}

	var result []string
	for index, part := range splitTop(params[1:matching(params, 0)], ",") {
		if len(part) == 0 {
			continue
		}

		if index == 0 && method && len(part) == 1 && (part[0].is("self") || part[0].is("cls")) {
			continue
		}

		if len(part) == 1 && (part[0].is("/") || part[0].is("*")) {
			result = append(result, part[0].text)
			continue
		}

		key, k := "", 0
		if part[0].is("*") || part[0].is("**") {
			key, k = part[0].text, 1
		}

		k++
		typeText, optional := "_", false
		if k < len(part) && part[k].is(":") {
			equals := untilTop(part, k+1, "=")
			typeText = collapse(spanText(src, part[k+1:equals]))
			k = equals
		}

		if k < len(part) && part[k].is("=") {
			optional = true
		}

		key += typeText
		if optional {
			key += "?"
		}

		result = append(result, key)
	}

	return result
}

// public reports whether a name is part of a contract: it does not start
// with an underscore, or it is a special name such as __init__.
func public(name string) bool {
	return !strings.HasPrefix(name, "_") || dunder(name)
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
		if previous >= 0 && current.start > previous {
			builder.WriteByte(' ')
		}

		builder.Write(src[current.start:current.end])
		previous = current.end
	}

	return builder.String()
}

// splitTop splits tokens at a separator outside brackets.
func splitTop(tokens []token, separator string) [][]token {
	var result [][]token
	start := 0
	for index := 0; index < len(tokens); {
		end := untilTop(tokens, index, separator)
		result = append(result, tokens[start:end])
		index = end + 1
		start = index
	}

	return result
}

// targetNames lists the plain names an assignment target binds: a, or a, b,
// or (a, (b, c)). Attribute and subscript targets bind no module name.
func targetNames(target []token) []token {
	if len(target) > 0 && (target[0].is("(") || target[0].is("[")) && matching(target, 0) == len(target)-1 {
		target = target[1 : len(target)-1]
	}

	var result []token
	for _, part := range splitTop(target, ",") {
		if len(part) > 0 && part[0].is("*") {
			part = part[1:]
		}

		switch {
		case len(part) == 1 && part[0].kind == tokenIdent && !isKeyword(part[0].text):
			result = append(result, part[0])
		case len(part) > 2 && (part[0].is("(") || part[0].is("[")) && matching(part, 0) == len(part)-1:
			result = append(result, targetNames(part)...)
		}
	}

	return result
}

// typeArguments keeps the type arguments of a base class, such as Foo in
// Generic[Foo], as references; the base itself becomes an embeds edge.
func typeArguments(base []token) []token {
	for index, current := range base {
		if current.is("[") {
			return base[index:]
		}
	}

	return nil
}

func unquote(current token) (string, bool) {
	text := current.text
	if current.kind != tokenString || len(text) < 2 {
		return "", false
	}

	quote := text[0]
	if (quote != '"' && quote != '\'') || text[len(text)-1] != quote {
		return "", false
	}

	return text[1 : len(text)-1], true
}

// untilTop returns the index of the first separator outside brackets, or
// len(tokens).
func untilTop(tokens []token, pos int, separator string) int {
	for pos < len(tokens) {
		current := tokens[pos]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			pos = matching(tokens, pos) + 1
			continue
		case current.text == separator && current.kind == tokenOperator:
			return pos
		}

		pos++
	}

	return pos
}

// valueKey is a variable's comparison key: its signature without its
// value, since a changed value keeps callers working.
func valueKey(signature string) string {
	head, _, _ := strings.Cut(signature, " = ")
	return head
}
