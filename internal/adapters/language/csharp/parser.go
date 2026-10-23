package csharp

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	memberClass       = "class"
	memberConst       = "const"
	memberConstructor = "constructor"
	memberDelegate    = "delegate"
	memberEnum        = "enum"
	memberEnumValue   = "enum value"
	memberEvent       = "event"
	memberField       = "field"
	memberIndexer     = "indexer"
	memberInterface   = "interface"
	memberMethod      = "method"
	memberOperator    = "operator"
	memberProgram     = "program"
	memberProperty    = "property"
	memberRecord      = "record"
	memberStruct      = "struct"
)

// programName names the symbol that owns a file's top-level statements.
const programName = "<Program>"

var (
	accessWords = []string{"public", "protected", "internal", "private", "file"}
	modifiers   = map[string]bool{
		"public": true, "protected": true, "internal": true, "private": true, "file": true,
		"static": true, "abstract": true, "sealed": true, "virtual": true, "override": true,
		"readonly": true, "const": true, "new": true, "partial": true, "unsafe": true,
		"extern": true, "async": true, "volatile": true, "required": true, "fixed": true,
		"ref": true, "implicit": true, "explicit": true, "scoped": true,
	}
	keywordTypes = map[string]string{
		"Boolean": "bool", "Byte": "byte", "SByte": "sbyte", "Char": "char", "Decimal": "decimal",
		"Double": "double", "Single": "float", "Int16": "short", "UInt16": "ushort", "Int32": "int",
		"UInt32": "uint", "Int64": "long", "UInt64": "ulong", "IntPtr": "nint", "UIntPtr": "nuint",
		"Object": "object", "String": "string",
	}
	reserved = map[string]bool{}
	xmlTag   = regexp.MustCompile(`<[^>]+>`)
)

func init() {
	for _, word := range strings.Fields(`abstract as base bool break byte case catch char checked class const
		continue decimal default delegate do double else enum event explicit extern false finally fixed
		float for foreach goto if implicit in int interface internal is lock long namespace new null
		object operator out override params private protected public readonly ref return sbyte sealed
		short sizeof stackalloc static string struct switch this throw true try typeof uint ulong
		unchecked unsafe ushort using virtual void volatile while`) {
		reserved[word] = true
	}
}

// sourceFile is everything one C# file declares.
type sourceFile struct {
	declarations []*declaration
	usings       []usingDirective
	program      bool
}

// usingDirective is one using directive: a namespace, a static type, or an
// alias, declared at a namespace level or at the file level.
type usingDirective struct {
	alias     string
	target    string
	tokens    []token
	static    bool
	global    bool
	namespace string
	line      int
}

// parameter is one parameter of a method, constructor, indexer or delegate.
type parameter struct {
	typeName string
	full     string
	modifier string
	value    string
	optional bool
	params   bool
	this     bool
}

// declaration is one type or member.
type declaration struct {
	kind       string
	member     string
	name       string
	simple     string
	owner      *declaration
	namespace  string
	signature  string
	modifiers  []string
	partial    bool
	assembly   bool
	directory  bool
	line       int
	endLine    int
	doc        string
	docLine    int
	bases      [][]token
	baseTexts  []string
	head       string
	where      string
	typeParams []string
	scope      []string
	parameters []parameter
	header     []token
	body       []token
	hidden     []token
	typeTokens []token
	accessors  []string
	declaring  bool
}

// parser reads one file's tokens.
type parser struct {
	code []token
	docs map[int][]token
	file *sourceFile
}

// parseFile reads one C# file's declarations and using directives.
//
// Errors:
//   - ErrUnparsable: the file does not lex.
func parseFile(src []byte) (*sourceFile, error) {
	tokens, err := lex(src)
	if err != nil {
		return nil, err
	}

	this := &parser{docs: map[int][]token{}, file: &sourceFile{}}
	var pending []token
	for _, current := range tokens {
		switch {
		case current.kind == tokenDoc:
			pending = append(pending, current)
		case current.trivia():
		default:
			if len(pending) > 0 {
				this.docs[len(this.code)] = pending
				pending = nil
			}

			this.code = append(this.code, current)
		}
	}

	this.members(0, len(this.code), nil, "", true)
	return this.file, nil
}

func (this *parser) at(index int) token {
	if index >= 0 && index < len(this.code) {
		return this.code[index]
	}

	return token{kind: tokenOperator}
}

// members reads the declarations between start and end: a file, a
// namespace block or a type body.
func (this *parser) members(start, end int, owner *declaration, namespace string, fileLevel bool) {
	if owner != nil && owner.member == memberEnum {
		this.enumMembers(start, end, owner)
		return
	}

	for index := start; index < end; {
		current := this.at(index)
		switch {
		case current.is(";"):
			index++
			continue
		case owner == nil && current.is("[") && this.at(index+2).is(":") && (this.at(index+1).is("assembly") || this.at(index+1).is("module")):
			index = this.skip(index)
			continue
		case owner == nil && (current.is("using") || current.is("global") && this.at(index+1).is("using")) && this.isUsing(index):
			index = this.using(index, namespace)
			continue
		case owner == nil && current.is("extern") && this.at(index+1).is("alias"):
			index = this.statementEnd(index, end)
			continue
		case owner == nil && current.is("namespace"):
			name, next := this.qualifiedName(index + 1)
			full := strings.Trim(namespace+"."+name, ".")
			if this.at(next).is("{") {
				close := this.skip(next) - 1
				this.members(next+1, close, nil, full, false)
				index = close + 1
				continue
			}

			namespace, index = full, next+1
			continue
		}

		declStart := index
		for this.at(index).is("[") {
			index = this.skip(index)
		}

		mods, next := this.modifiers(index, end)
		keyword := this.at(next)
		switch {
		case keyword.is("class") || keyword.is("struct") || keyword.is("interface") || keyword.is("enum") ||
			keyword.is("record") && (this.at(next+1).kind == tokenIdent) || keyword.is("delegate") && !this.at(next+1).is("("):
			index = this.typeDeclaration(declStart, index, mods, next, owner, namespace)
		case owner == nil:
			stop := this.statementEnd(declStart, end)
			if fileLevel {
				this.statements(declStart, stop)
			}

			index = stop
		default:
			index = this.member(declStart, index, mods, next, end, owner)
		}

		if index <= declStart {
			index = declStart + 1
		}
	}
}

// isUsing tells a using directive from a using statement.
func (this *parser) isUsing(index int) bool {
	if this.at(index).is("global") {
		index++
	}

	next := this.at(index + 1)
	return next.kind == tokenIdent && !next.is("var") && !next.is("await")
}

// using records one using directive and returns the index after it.
func (this *parser) using(index int, namespace string) int {
	directive := usingDirective{namespace: namespace, line: this.at(index).line}
	if this.at(index).is("global") {
		directive.global = true
		index++
	}

	index++
	if this.at(index).is("static") {
		directive.static = true
		index++
	}

	if this.at(index).kind == tokenIdent && this.at(index+1).is("=") {
		directive.alias = identifier(this.at(index).text)
		index += 2
	}

	end := index
	for end < len(this.code) && !this.at(end).is(";") {
		end++
	}

	directive.tokens = this.code[index:end]
	directive.target = render(directive.tokens)
	this.file.usings = append(this.file.usings, directive)
	return end + 1
}

// qualifiedName reads A.B.C and returns it with the index after it.
func (this *parser) qualifiedName(index int) (string, int) {
	var parts []string
	for this.at(index).kind == tokenIdent {
		parts = append(parts, identifier(this.at(index).text))
		if !this.at(index + 1).is(".") {
			index++
			break
		}

		index += 2
	}

	return strings.Join(parts, "."), index
}

// modifiers reads the modifier words at index and returns them with the
// index after them. A contextual word is a modifier only when a
// declaration follows it.
func (this *parser) modifiers(index, end int) ([]string, int) {
	var result []string
	for index < end {
		current := this.at(index)
		if current.kind != tokenIdent || !modifiers[current.text] {
			break
		}

		next := this.at(index + 1)
		if next.is("(") || next.is("=") || next.is(";") || next.is(",") || next.is(".") || next.is(")") {
			break
		}

		result = append(result, current.text)
		index++
	}

	return result, index
}

// skip returns the index after the bracket group that opens at index.
func (this *parser) skip(index int) int {
	depth := 0
	for ; index < len(this.code); index++ {
		current := this.code[index]
		if current.kind != tokenOperator {
			continue
		}

		switch current.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}

	return index
}

// skipAngle returns the index after the type argument list that opens at
// index.
func (this *parser) skipAngle(index int) int {
	depth := 0
	for ; index < len(this.code); index++ {
		current := this.code[index]
		switch {
		case current.is("<"):
			depth++
		case current.is(">"):
			depth--
			if depth == 0 {
				return index + 1
			}
		case current.is("(") || current.is("["):
			index = this.skip(index) - 1
		case current.is(";") || current.is("{") || current.is("}"):
			return index
		}
	}

	return index
}

// statementEnd returns the index after the statement that starts at index:
// after a ; at depth 0, or after a block that closes at depth 0.
func (this *parser) statementEnd(index, end int) int {
	depth := 0
	for ; index < end; index++ {
		current := this.code[index]
		if current.kind != tokenOperator {
			continue
		}

		switch current.text {
		case "(", "[", "{":
			depth++
		case ")", "]":
			depth--
		case "}":
			depth--
			if depth == 0 {
				return index + 1
			}
		case ";":
			if depth == 0 {
				return index + 1
			}
		}
	}

	return end
}

// statements adds top-level statements to the file's program symbol.
func (this *parser) statements(start, end int) {
	this.file.program = true
	var program *declaration
	for _, each := range this.file.declarations {
		if each.member == memberProgram {
			program = each
		}
	}

	if program == nil {
		program = &declaration{kind: graph.KindFunction, member: memberProgram, name: programName, simple: programName, signature: programName, line: this.at(start).line}
		this.file.declarations = append(this.file.declarations, program)
	}

	program.body = append(program.body, this.code[start:end]...)
	program.endLine = this.at(end - 1).line
}

// add records a declaration, with its access and doc comment.
func (this *parser) add(result *declaration, declStart int) *declaration {
	result.assembly, result.directory = access(result)
	if docs := this.docs[declStart]; len(docs) > 0 {
		result.doc, result.docLine = docText(docs), docs[0].line
	}

	this.file.declarations = append(this.file.declarations, result)
	return result
}

// typeDeclaration reads a class, struct, interface, enum, record or
// delegate, with its body, and returns the index after it.
func (this *parser) typeDeclaration(declStart, modStart int, mods []string, index int, owner *declaration, namespace string) int {
	result := &declaration{kind: graph.KindType, owner: owner, namespace: namespace, modifiers: mods, partial: slices.Contains(mods, "partial"), line: this.at(index).line}
	keyword := this.at(index)
	result.member = keyword.text
	head := []token{keyword}
	index++
	switch {
	case result.member == "interface":
		result.kind = graph.KindInterface
	case result.member == "record" && this.at(index).is("struct"):
		result.member = "record struct"
		head = append(head, this.at(index))
		index++
	case result.member == "record" && this.at(index).is("class"):
		index++
	}

	if result.member == memberDelegate {
		end := this.typeEnd(index)
		head = append(head, this.code[index:end]...)
		index = end
	}

	result.simple = identifier(this.at(index).text)
	head = append(head, this.at(index))
	index++
	if this.at(index).is("<") {
		end := this.skipAngle(index)
		head = append(head, this.code[index:end]...)
		for _, part := range splitTop(this.code[index+1 : end-1]) {
			if len(part) > 0 {
				result.typeParams = append(result.typeParams, identifier(part[len(part)-1].text))
			}
		}

		index = end
	}

	result.name = result.simple + arity(len(result.typeParams))
	if owner != nil {
		result.name = owner.name + "." + result.name
		result.scope = append(result.scope, owner.scope...)
	}

	result.scope = append(result.scope, result.typeParams...)
	var primary []token
	if this.at(index).is("(") {
		end := this.skip(index)
		primary = this.code[index:end]
		if result.member == memberDelegate {
			head = append(head, primary...)
			result.parameters = this.parameters(primary, result.scope, nil)
		}

		index = end
	}

	if this.at(index).is(":") {
		index++
		start := index
		for index < len(this.code) && !this.at(index).is("{") && !this.at(index).is(";") && !this.at(index).is("where") {
			if this.at(index).is("<") {
				index = this.skipAngle(index)
				continue
			}

			if this.at(index).is("(") {
				index = this.skip(index)
				continue
			}

			index++
		}

		for _, part := range splitTop(this.code[start:index]) {
			if paren := slices.IndexFunc(part, func(each token) bool { return each.is("(") }); paren > 0 {
				part = part[:paren]
			}

			result.bases = append(result.bases, part)
		}

		for _, part := range result.bases {
			result.baseTexts = append(result.baseTexts, render(part))
		}
	}

	whereStart := index
	for index < len(this.code) && !this.at(index).is("{") && !this.at(index).is(";") {
		index++
	}

	result.head = render(head)
	result.where = render(this.code[whereStart:index])
	result.header = this.code[declStart:index]
	result.signature = typeSignature(result)
	this.add(result, declStart)
	if primary != nil && result.member != memberDelegate {
		this.primary(result, primary, declStart)
	}

	result.endLine = this.at(index).line
	switch {
	case this.at(index).is("{"):
		close := this.skip(index) - 1
		this.members(index+1, close, result, namespace, false)
		result.endLine = this.at(close).line
		index = close + 1
		if this.at(index).is(";") {
			index++
		}
	case this.at(index).is(";"):
		index++
	}

	return index
}

// typeSignature writes a type's signature from its modifiers, head, base
// list and constraints.
func typeSignature(result *declaration) string {
	text := strings.Join(append(append([]string{}, result.modifiers...), result.head), " ")
	if len(result.baseTexts) > 0 {
		text += " : " + strings.Join(result.baseTexts, ", ")
	}

	if result.where != "" {
		text += " " + result.where
	}

	return withAccess(text, result)
}

// primary adds a primary constructor, and for a record the properties its
// parameters make.
func (this *parser) primary(owner *declaration, tokens []token, declStart int) {
	parameters := this.parameters(tokens, owner.scope, nil)
	var names []string
	for _, each := range parameters {
		names = append(names, each.typeName)
	}

	constructor := &declaration{
		kind: graph.KindMethod, member: memberConstructor, owner: owner, namespace: owner.namespace,
		simple: owner.simple, name: owner.name + "." + owner.simple + "(" + strings.Join(names, ",") + ")",
		signature: "public " + owner.simple + render(tokens), modifiers: []string{"public"},
		parameters: parameters, header: tokens, line: tokens[0].line, endLine: tokens[len(tokens)-1].line,
	}

	this.add(constructor, -1)
	if !strings.HasPrefix(owner.member, "record") {
		return
	}

	accessors := "{ get; init; }"
	if owner.member == "record struct" && !slices.Contains(owner.modifiers, "readonly") {
		accessors = "{ get; set; }"
	}

	for _, part := range splitTop(tokens[1 : len(tokens)-1]) {
		part = withoutAttributes(part)
		if len(part) < 2 {
			continue
		}

		name := part[len(part)-1]
		if eq := slices.IndexFunc(part, func(each token) bool { return each.is("=") }); eq > 0 {
			name = part[eq-1]
			part = part[:eq]
		}

		property := &declaration{
			kind: graph.KindValue, member: memberProperty, owner: owner, namespace: owner.namespace,
			simple: identifier(name.text), name: owner.name + "." + identifier(name.text),
			signature: "public " + render(part[:len(part)-1]) + " " + printed(name) + " " + accessors,
			modifiers: []string{"public"}, header: part, line: name.line, endLine: name.line,
		}

		this.add(property, -1)
	}
}

// enumMembers reads an enum body and returns its end. A member with no
// value gets the one after the member before it, when that one is known.
func (this *parser) enumMembers(index, end int, owner *declaration) int {
	zero := int64(0)
	previous, first := &zero, true
	for index < end {
		declStart, stop := index, index
		for stop < end && !this.at(stop).is(",") {
			if this.at(stop).is("(") || this.at(stop).is("[") || this.at(stop).is("{") {
				stop = this.skip(stop)
				continue
			}

			stop++
		}

		part := withoutAttributes(this.code[index:stop])
		index = stop + 1
		if len(part) == 0 || part[0].kind != tokenIdent {
			continue
		}

		name := part[0]
		result := &declaration{
			kind: graph.KindValue, member: memberEnumValue, owner: owner, namespace: owner.namespace,
			simple: identifier(name.text), name: owner.name + "." + identifier(name.text),
			header: part, line: name.line, endLine: part[len(part)-1].line,
		}

		signature := "public " + printed(name)
		switch {
		case len(part) > 2 && part[1].is("="):
			signature += " = " + render(part[2:])
			result.body = part[2:]
			previous = literal(part[2:])
		case previous != nil:
			next := *previous + 1
			if first {
				next = 0
			}

			previous = &next
			signature += " = " + strconv.FormatInt(next, 10)
		}

		first = false
		result.signature = signature
		this.add(result, declStart)
	}

	return end
}

// literal reads an integer literal, with an optional minus sign.
func literal(tokens []token) *int64 {
	negative := false
	if len(tokens) == 2 && tokens[0].is("-") {
		negative, tokens = true, tokens[1:]
	}

	if len(tokens) != 1 || tokens[0].kind != tokenNumber {
		return nil
	}

	text := strings.TrimRight(strings.ReplaceAll(tokens[0].text, "_", ""), "uUlL")
	value, err := strconv.ParseInt(text, 0, 64)
	if err != nil {
		return nil
	}

	if negative {
		value = -value
	}

	return &value
}

// member reads one member of a class, struct, record or interface and
// returns the index after it.
func (this *parser) member(declStart, modStart int, mods []string, index, end int, owner *declaration) int {
	headerEnd := index
	for headerEnd < end {
		current := this.at(headerEnd)
		if current.is("{") || current.is(";") || current.is("=>") || current.is("=") {
			break
		}

		if current.is("(") || current.is("[") {
			headerEnd = this.skip(headerEnd)
			continue
		}

		headerEnd++
	}

	result := &declaration{owner: owner, namespace: owner.namespace, modifiers: mods, partial: slices.Contains(mods, "partial"), line: this.at(index).line}
	hide := func(stop int) int {
		owner.hidden = append(owner.hidden, this.code[declStart:stop]...)
		return stop
	}

	switch {
	case this.at(index).is("~"):
		return hide(this.memberEnd(headerEnd, end, false))
	case this.at(index).kind == tokenIdent && identifier(this.at(index).text) == owner.simple && this.at(index+1).is("("):
		close := this.skip(index + 1)
		stop := this.memberEnd(headerEnd, end, false)
		if slices.Contains(mods, "static") {
			return hide(stop)
		}

		result.kind, result.member, result.simple = graph.KindMethod, memberConstructor, owner.simple
		result.parameters = this.parameters(this.code[index+1:close], owner.scope, nil)
		result.name = owner.name + "." + owner.simple + parameterList(result.parameters)
		result.signature = withAccess(render(withoutAttributes(this.code[modStart:close])), result)
		result.header = this.code[declStart:close]
		result.body = this.code[close:stop]
		result.endLine = this.at(stop - 1).line
		this.add(result, declStart)
		return stop
	case this.at(index).is("event"):
		return this.event(declStart, modStart, index, headerEnd, end, result)
	}

	operator := slices.IndexFunc(this.code[index:headerEnd], func(each token) bool { return each.is("operator") })
	if operator >= 0 {
		return this.operator(declStart, modStart, index, index+operator, headerEnd, end, result)
	}

	typeEnd := this.typeEnd(index)
	result.typeTokens = this.code[index:typeEnd]
	if this.at(typeEnd).kind != tokenIdent || typeEnd >= headerEnd {
		return this.memberEnd(headerEnd, end, false)
	}

	// An explicit interface implementation qualifies its name, as in
	// IStore.Save or IList<T>.this.
	nameStart, explicit := typeEnd, false
	for this.at(nameStart).kind == tokenIdent && !this.at(nameStart).is("this") {
		next := nameStart + 1
		if this.at(next).is("<") {
			next = this.skipAngle(next)
		}

		if !this.at(next).is(".") || this.at(next+1).kind != tokenIdent {
			break
		}

		explicit, nameStart = true, next+1
	}

	if explicit {
		return hide(this.memberEnd(headerEnd, end, this.at(headerEnd).is("{")))
	}

	name := this.at(nameStart)
	result.simple = identifier(name.text)
	after := this.at(nameStart + 1)
	scope := owner.scope
	switch {
	case name.is("this") && after.is("["):
		close := this.skip(nameStart + 1)
		result.kind, result.member = graph.KindValue, memberIndexer
		result.parameters = this.parameters(this.code[nameStart+1:close], scope, nil)
		var names []string
		for _, each := range result.parameters {
			names = append(names, each.typeName)
		}

		result.simple = "this[" + strings.Join(names, ",") + "]"
		result.name = owner.name + "." + result.simple
		return this.property(declStart, modStart, close, headerEnd, end, result)
	case after.is("(") || after.is("<"):
		var methodParams []string
		open := nameStart + 1
		if after.is("<") {
			close := this.skipAngle(open)
			for _, part := range splitTop(this.code[open+1 : close-1]) {
				if len(part) > 0 {
					methodParams = append(methodParams, identifier(part[len(part)-1].text))
				}
			}

			open = close
		}

		if !this.at(open).is("(") {
			return this.memberEnd(headerEnd, end, false)
		}

		close := this.skip(open)
		result.kind, result.member, result.typeParams = graph.KindMethod, memberMethod, methodParams
		result.parameters = this.parameters(this.code[open:close], scope, methodParams)
		result.name = owner.name + "." + result.simple + arity(len(methodParams)) + parameterList(result.parameters)
		stop := this.memberEnd(headerEnd, end, false)
		result.signature = withAccess(render(withoutAttributes(this.code[modStart:headerEnd])), result)
		result.header = this.code[declStart:headerEnd]
		result.body = bodyTokens(this.code[headerEnd:stop])
		result.declaring = this.at(headerEnd).is(";")
		result.endLine = this.at(stop - 1).line
		if result.simple == "Main" && slices.Contains(mods, "static") {
			this.file.program = true
		}

		this.add(result, declStart)
		return stop
	case after.is("{") || after.is("=>"):
		result.kind, result.member = graph.KindValue, memberProperty
		result.name = owner.name + "." + result.simple
		return this.property(declStart, modStart, nameStart+1, headerEnd, end, result)
	default:
		return this.fields(declStart, modStart, typeEnd, end, result)
	}
}

// property reads the accessors of a property or indexer whose header ends
// before index.
func (this *parser) property(declStart, modStart, index, headerEnd, end int, result *declaration) int {
	header := withAccess(render(withoutAttributes(this.code[modStart:index])), result)
	stop := index
	var accessors []string
	switch {
	case this.at(index).is("=>"):
		stop = this.statementEnd(index, end)
		accessors = []string{"get;"}
		result.accessors = []string{"get"}
		result.body = bodyTokens(this.code[index:stop])
	case this.at(index).is("{"):
		close := this.skip(index) - 1
		for at := index + 1; at < close; {
			for this.at(at).is("[") {
				at = this.skip(at)
			}

			var words []string
			for at < close && this.at(at).kind == tokenIdent {
				words = append(words, this.at(at).text)
				at++
			}

			if len(words) > 0 {
				result.accessors = append(result.accessors, strings.Join(words, " "))
				accessors = append(accessors, strings.Join(words, " ")+";")
			}

			bodyStart := at
			switch {
			case this.at(at).is("{"):
				at = this.skip(at)
			case this.at(at).is("=>"):
				at = this.statementEnd(at, close)
			default:
				at++
			}

			result.body = append(result.body, this.code[bodyStart:at]...)
		}

		stop = close + 1
		if this.at(stop).is("=") {
			initEnd := this.statementEnd(stop, end)
			result.body = append(result.body, this.code[stop:initEnd]...)
			stop = initEnd
		}
	default:
		stop = this.memberEnd(headerEnd, end, false)
	}

	result.signature = header + " { " + strings.Join(accessors, " ") + " }"
	result.header = this.code[declStart:index]
	result.endLine = this.at(stop - 1).line
	this.add(result, declStart)
	return stop
}

// event reads an event, field-like or with accessors.
func (this *parser) event(declStart, modStart, index, headerEnd, end int, result *declaration) int {
	result.kind, result.member = graph.KindValue, memberEvent
	typeEnd := this.typeEnd(index + 1)
	result.typeTokens = this.code[index+1 : typeEnd]
	if this.at(typeEnd + 1).is(".") {
		owner := result.owner
		stop := this.memberEnd(headerEnd, end, this.at(headerEnd).is("{"))
		owner.hidden = append(owner.hidden, this.code[declStart:stop]...)
		return stop
	}

	if this.at(headerEnd).is("{") {
		name := this.at(typeEnd)
		result.simple = identifier(name.text)
		result.name = result.owner.name + "." + result.simple
		stop := this.skip(headerEnd)
		result.signature = withAccess(render(withoutAttributes(this.code[modStart:headerEnd])), result)
		result.header = this.code[declStart:headerEnd]
		result.body = this.code[headerEnd:stop]
		result.endLine = this.at(stop - 1).line
		this.add(result, declStart)
		return stop
	}

	return this.fields(declStart, modStart, typeEnd, end, result)
}

// fields reads one or more field declarators that follow a type, as in
// int a = 1, b;
func (this *parser) fields(declStart, modStart, typeEnd, end int, template *declaration) int {
	stop := this.statementEnd(typeEnd, end)
	declarators := this.code[typeEnd:stop]
	if len(declarators) > 0 && declarators[len(declarators)-1].is(";") {
		declarators = declarators[:len(declarators)-1]
	}
	if template.member == "" {
		template.kind, template.member = graph.KindValue, memberField
		if slices.Contains(template.modifiers, "const") {
			template.member = memberConst
		}
	}

	prefix := withoutAttributes(this.code[modStart:typeEnd])
	for _, part := range splitTop(declarators) {
		if len(part) == 0 || part[0].kind != tokenIdent {
			continue
		}

		result := *template
		result.simple = identifier(part[0].text)
		result.name = template.owner.name + "." + result.simple
		if eq := slices.IndexFunc(part, func(each token) bool { return each.is("=") }); eq > 0 {
			result.body = part[eq+1:]
		}

		result.signature = withAccess(render(append(append([]token{}, prefix...), part...)), &result)
		result.header = append(append([]token{}, this.code[declStart:typeEnd]...), part[0])
		result.line = part[0].line
		result.endLine = part[len(part)-1].line
		this.add(&result, declStart)
	}

	return stop
}

// operator reads an operator or a conversion.
func (this *parser) operator(declStart, modStart, index, keyword, headerEnd, end int, result *declaration) int {
	owner := result.owner
	at := keyword + 1
	checked := false
	if this.at(at).is("checked") {
		checked = true
		at++
	}

	conversion := slices.Contains(result.modifiers, "implicit") || slices.Contains(result.modifiers, "explicit")
	var symbol string
	var returns string
	if conversion {
		typeEnd := this.typeEnd(at)
		returns = typeName(this.code[at:typeEnd], owner.scope, nil)
		result.typeTokens = this.code[at:typeEnd]
		symbol = "Implicit"
		if slices.Contains(result.modifiers, "explicit") {
			symbol = "Explicit"
		}

		at = typeEnd
	} else {
		text := this.at(at).text
		for next := at + 1; this.at(next).kind == tokenOperator && this.at(next).start == this.at(next-1).end && !this.at(next).is("("); next++ {
			text += this.at(next).text
			at = next
		}

		at++
		symbol = text
		result.typeTokens = this.code[index:keyword]
	}

	if !this.at(at).is("(") {
		return this.memberEnd(headerEnd, end, false)
	}

	close := this.skip(at)
	result.kind, result.member = graph.KindMethod, memberOperator
	result.parameters = this.parameters(this.code[at:close], owner.scope, nil)
	if !conversion {
		symbol = operatorName(symbol, len(result.parameters))
	}

	if checked {
		symbol = "Checked" + symbol
	}

	result.simple = "op_" + symbol
	result.name = owner.name + "." + result.simple + parameterList(result.parameters) + returns
	stop := this.memberEnd(headerEnd, end, false)
	result.signature = withAccess(render(withoutAttributes(this.code[modStart:headerEnd])), result)
	result.header = this.code[declStart:headerEnd]
	result.body = bodyTokens(this.code[headerEnd:stop])
	result.endLine = this.at(stop - 1).line
	this.add(result, declStart)
	return stop
}

// memberEnd returns the index after the body that starts at index: a
// block, an expression body or a semicolon. A property-like block may be
// followed by an initializer.
func (this *parser) memberEnd(index, end int, initializer bool) int {
	switch {
	case this.at(index).is("{"):
		stop := this.skip(index)
		if initializer && this.at(stop).is("=") {
			return this.statementEnd(stop, end)
		}

		return stop
	case this.at(index).is(";"):
		return index + 1
	default:
		return this.statementEnd(index, end)
	}
}

// typeEnd returns the index after the type that starts at index.
func (this *parser) typeEnd(index int) int {
	for this.at(index).is("ref") || this.at(index).is("readonly") || this.at(index).is("scoped") {
		index++
	}

	switch {
	case this.at(index).is("("):
		index = this.skip(index)
	case this.at(index).kind == tokenIdent:
		for {
			index++
			if this.at(index).is("<") {
				index = this.skipAngle(index)
			}

			if !(this.at(index).is(".") || this.at(index).is("::")) || this.at(index+1).kind != tokenIdent {
				break
			}

			index++
		}
	default:
		return index
	}

	for {
		switch {
		case this.at(index).is("?") || this.at(index).is("*"):
			index++
		case this.at(index).is("[") && (this.at(index+1).is("]") || this.at(index+1).is(",")):
			index = this.skip(index)
		default:
			return index
		}
	}
}

// parameters reads a parenthesized or bracketed parameter list.
func (this *parser) parameters(tokens []token, scope, methodParams []string) []parameter {
	if len(tokens) < 2 {
		return nil
	}

	var result []parameter
	for _, part := range splitTop(tokens[1 : len(tokens)-1]) {
		part = withoutAttributes(part)
		if len(part) == 0 {
			continue
		}

		var each parameter
		reference := false
		var words []string
	modifiers:
		for len(part) > 0 && part[0].kind == tokenIdent {
			switch part[0].text {
			case "this":
				each.this = true
			case "params":
				each.params = true
			case "ref", "out", "in":
				reference = true
			case "scoped", "readonly":
			default:
				break modifiers
			}

			words = append(words, part[0].text)
			part = part[1:]
		}

		end := len(part)
		if eq := slices.IndexFunc(part, func(current token) bool { return current.is("=") }); eq >= 0 {
			each.optional = true
			each.value = render(part[eq+1:])
			end = eq
		}

		if end > 1 && part[end-1].kind == tokenIdent {
			end--
		}

		each.modifier = strings.Join(words, " ")
		each.full = fullType(part[:end], scope, methodParams)
		each.typeName = typeName(part[:end], scope, methodParams)
		if reference {
			each.typeName += "&"
		}

		result = append(result, each)
	}

	return result
}

// typeName writes a type for a symbol name: the last segment of each
// qualified name, C# keywords for .NET names, positional type parameters,
// and no tuple element names or spaces.
func typeName(tokens []token, scope, methodParams []string) string {
	return readType(&typeReader{tokens: tokens, scope: scope, methodParams: methodParams})
}

// fullType writes a type for a compare key: as typeName, but qualified
// names keep their qualifiers, so Outer.Inner and Other.Inner differ.
func fullType(tokens []token, scope, methodParams []string) string {
	return readType(&typeReader{tokens: tokens, scope: scope, methodParams: methodParams, full: true})
}

func readType(reader *typeReader) string {
	tokens := reader.tokens
	var parts []string
	for reader.index < len(tokens) {
		start := reader.index
		parts = append(parts, reader.read())
		if reader.index == start {
			parts = append(parts, tokens[start].text)
			reader.index++
		}
	}

	return strings.Join(parts, "")
}

// typeReader reads the types inside one token list.
type typeReader struct {
	tokens       []token
	index        int
	scope        []string
	methodParams []string
	full         bool
}

func (this *typeReader) at(offset int) token {
	if this.index+offset < len(this.tokens) {
		return this.tokens[this.index+offset]
	}

	return token{kind: tokenOperator}
}

func (this *typeReader) read() string {
	var result string
	switch {
	case this.at(0).is("("):
		this.index++
		var elements []string
		for this.index < len(this.tokens) && !this.at(0).is(")") {
			elements = append(elements, this.read())
			if this.at(0).kind == tokenIdent {
				this.index++
			}

			if this.at(0).is(",") {
				this.index++
			}
		}

		this.index++
		result = "(" + strings.Join(elements, ",") + ")"
	case this.at(0).kind == tokenIdent:
		var qualifiers []string
		for {
			name := identifier(this.at(0).text)
			this.index++
			var arguments []string
			if this.at(0).is("<") {
				this.index++
				for this.index < len(this.tokens) && !this.at(0).is(">") {
					arguments = append(arguments, this.read())
					if this.at(0).is(",") {
						this.index++
					}
				}

				this.index++
			}

			separator := this.at(0)
			if !(separator.is(".") || separator.is("::")) || this.at(1).kind != tokenIdent {
				system := len(qualifiers) == 1 && qualifiers[0] == "System" && keywordTypes[name] != "" && len(arguments) == 0
				if this.full && len(qualifiers) > 0 && !system {
					result = strings.Join(qualifiers, ".") + "." + name + typeArgumentText(arguments)
				} else {
					result = this.simple(name, arguments)
				}

				break
			}

			if separator.is(".") {
				qualifiers = append(qualifiers, name+typeArgumentText(arguments))
			}

			this.index++
		}
	default:
		return ""
	}

	for {
		switch {
		case this.at(0).is("?") || this.at(0).is("*"):
			result += this.at(0).text
			this.index++
		case this.at(0).is("["):
			result += "["
			this.index++
			for this.index < len(this.tokens) && !this.at(0).is("]") {
				result += this.at(0).text
				this.index++
			}

			result += "]"
			this.index++
		default:
			return result
		}
	}
}

// simple writes one name with its type arguments.
func (this *typeReader) simple(name string, arguments []string) string {
	if len(arguments) == 0 {
		if at := slices.Index(this.methodParams, name); at >= 0 {
			return "M" + strconv.Itoa(at)
		}

		if at := slices.Index(this.scope, name); at >= 0 {
			return "T" + strconv.Itoa(lastIndex(this.scope, name))
		}

		if keyword, ok := keywordTypes[name]; ok {
			return keyword
		}

		return name
	}

	return name + typeArgumentText(arguments)
}

func typeArgumentText(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}

	return "<" + strings.Join(arguments, ",") + ">"
}

// lastIndex finds the innermost type parameter with a name, since an inner
// type's parameter hides an outer one.
func lastIndex(scope []string, name string) int {
	for index := len(scope) - 1; index >= 0; index-- {
		if scope[index] == name {
			return index
		}
	}

	return -1
}

// operatorName gives the .NET name of an operator.
func operatorName(symbol string, count int) string {
	unary := map[string]string{"+": "UnaryPlus", "-": "UnaryNegation", "!": "LogicalNot", "~": "OnesComplement", "++": "Increment", "--": "Decrement", "true": "True", "false": "False"}
	binary := map[string]string{
		"+": "Addition", "-": "Subtraction", "*": "Multiply", "/": "Division", "%": "Modulus",
		"&": "BitwiseAnd", "|": "BitwiseOr", "^": "ExclusiveOr", "<<": "LeftShift", ">>": "RightShift", ">>>": "UnsignedRightShift",
		"==": "Equality", "!=": "Inequality", "<": "LessThan", ">": "GreaterThan", "<=": "LessThanOrEqual", ">=": "GreaterThanOrEqual",
		"+=": "AdditionAssignment", "-=": "SubtractionAssignment", "*=": "MultiplicationAssignment", "/=": "DivisionAssignment",
		"%=": "ModulusAssignment", "&=": "BitwiseAndAssignment", "|=": "BitwiseOrAssignment", "^=": "ExclusiveOrAssignment",
		"<<=": "LeftShiftAssignment", ">>=": "RightShiftAssignment", ">>>=": "UnsignedRightShiftAssignment",
	}

	if name, ok := unary[symbol]; ok && (count == 1 || symbol == "++" || symbol == "--" || symbol == "true" || symbol == "false") {
		return name
	}

	if name, ok := binary[symbol]; ok {
		return name
	}

	return symbol
}

// access gives a declaration's two access flags: other assemblies can use
// it, and other directories of its project can use it. A nested
// declaration also needs its owner's flags.
func access(result *declaration) (assembly, directory bool) {
	words := strings.Join(accessOf(result.modifiers), " ")
	switch words {
	case "public", "protected", "protected internal", "internal protected":
		assembly, directory = true, true
	case "internal", "private protected", "protected private":
		assembly, directory = false, true
	case "private", "file":
		assembly, directory = false, false
	default:
		switch {
		case result.member == memberProgram:
		case result.owner == nil:
			directory = true
		case result.owner.member == memberInterface || result.owner.member == memberEnum:
			assembly, directory = true, true
		}
	}

	if result.owner != nil {
		assembly = assembly && result.owner.assembly
		directory = directory && result.owner.directory
	}

	return assembly, directory
}

// accessOf keeps the access words of a modifier list.
func accessOf(mods []string) []string {
	var result []string
	for _, each := range mods {
		if slices.Contains(accessWords, each) {
			result = append(result, each)
		}
	}

	return result
}

// withAccess puts the default visibility in front of a signature that has
// no access modifier.
func withAccess(signature string, result *declaration) string {
	if len(accessOf(result.modifiers)) > 0 || result.member == memberEnumValue {
		return signature
	}

	switch {
	case result.owner == nil:
		return "internal " + signature
	case result.owner.member == memberInterface:
		return "public " + signature
	default:
		return "private " + signature
	}
}

// render joins tokens into one line. Tokens that were apart in the source
// get one space between them, except inside brackets, and literals keep
// their text with control characters escaped.
func render(tokens []token) string {
	var builder strings.Builder
	for index, current := range tokens {
		if index > 0 {
			previous := tokens[index-1]
			if current.start != previous.end && !previous.is("(") && !previous.is("[") && !current.is(")") && !current.is("]") && !current.is(",") {
				builder.WriteByte(' ')
			}
		}

		builder.WriteString(printed(current))
	}

	return builder.String()
}

// printed writes one token as a signature shows it.
func printed(current token) string {
	switch current.kind {
	case tokenString, tokenChar:
		return strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(current.text)
	case tokenIdent:
		if strings.HasPrefix(current.text, "@") && !reserved[current.text[1:]] {
			return current.text[1:]
		}
	}

	return current.text
}

// identifier drops the @ of a verbatim identifier.
func identifier(text string) string {
	return strings.TrimPrefix(text, "@")
}

// withoutAttributes drops attribute sections: a bracket group at the start,
// or after (, comma, < or another attribute.
func withoutAttributes(tokens []token) []token {
	var result []token
	for index := 0; index < len(tokens); index++ {
		previous := ""
		if len(result) > 0 {
			previous = result[len(result)-1].text
		}

		if tokens[index].is("[") && (len(result) == 0 || previous == "(" || previous == "," || previous == "<") {
			depth := 0
			for ; index < len(tokens); index++ {
				if tokens[index].is("[") {
					depth++
				} else if tokens[index].is("]") {
					depth--
					if depth == 0 {
						break
					}
				}
			}

			continue
		}

		result = append(result, tokens[index])
	}

	return result
}

// splitTop splits tokens at commas outside brackets and type arguments.
func splitTop(tokens []token) [][]token {
	var result [][]token
	depth, start := 0, 0
	for index, current := range tokens {
		if current.kind != tokenOperator {
			continue
		}

		switch current.text {
		case "(", "[", "{", "<":
			depth++
		case ")", "]", "}", ">":
			depth--
		case ",":
			if depth == 0 {
				result = append(result, tokens[start:index])
				start = index + 1
			}
		}
	}

	if start < len(tokens) {
		result = append(result, tokens[start:])
	}

	return result
}

// bodyTokens drops the => and ; around an expression body and the braces
// around a block.
func bodyTokens(tokens []token) []token {
	if len(tokens) > 0 && (tokens[0].is("=>") || tokens[0].is("{")) {
		tokens = tokens[1:]
	}

	if len(tokens) > 0 && (tokens[len(tokens)-1].is(";") || tokens[len(tokens)-1].is("}")) {
		tokens = tokens[:len(tokens)-1]
	}

	return tokens
}

// parameterList writes the parameter types of a name.
func parameterList(parameters []parameter) string {
	var names []string
	for _, each := range parameters {
		names = append(names, each.typeName)
	}

	return "(" + strings.Join(names, ",") + ")"
}

// arity writes the open generic form: <> for one type parameter, <,> for
// two.
func arity(count int) string {
	if count == 0 {
		return ""
	}

	return "<" + strings.Repeat(",", count-1) + ">"
}

// docText reads /// lines as plain text without XML tags.
func docText(docs []token) string {
	var lines []string
	for _, each := range docs {
		line := strings.TrimPrefix(each.text, "///")
		line = strings.TrimSpace(xmlTag.ReplaceAllString(line, ""))
		if line != "" {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "\n")
}
