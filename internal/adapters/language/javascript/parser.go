package javascript

import (
	"regexp"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

var whitespace = regexp.MustCompile(`\s+`)

// binding is one name an import brings in: the local name and the name the
// other file exports, "default" for its default export or "*" for the whole
// module.
type binding struct {
	local    string
	imported string
}

// declaration is one top-level declaration, or one class or interface
// member.
type declaration struct {
	kind         string
	name         string
	owner        *declaration
	exported     bool
	private      bool
	signature    string
	key          string
	fields       []graph.Field
	fieldPrivate []bool
	extends      [][]token
	implements   [][]token
	line         int
	endLine      int
	docLine      int
	hashText     string
	refs         []token
}

// function is the parsed shape of a function, method or arrow function.
type function struct {
	text       string
	async      bool
	typeParams []token
	params     []token
	result     []token
	body       []token
}

// importSpec is one import, re-export, require or dynamic import.
type importSpec struct {
	path     string
	line     int
	names    []binding
	reexport bool
	all      bool
	require  bool
}

// sourceFile is everything the extractor needs from one file.
type sourceFile struct {
	src          []byte
	imports      []importSpec
	exports      map[string]string
	declarations []*declaration
}

// parser walks the tokens of one file, one top-level statement at a time.
type parser struct {
	src     []byte
	tokens  []token
	pos     int
	file    *sourceFile
	bound   map[int]bool
	pending []token
}

// parseFile reads the contract-level structure of one file.
//
// Parameters:
//   - src: the source.
//   - jsx: whether the file may hold JSX.
//
// Returns:
//   - result: its imports, exports and declarations.
func parseFile(src []byte, jsx bool) *sourceFile {
	this := &parser{src: src, tokens: lex(src, jsx), file: &sourceFile{src: src, exports: map[string]string{}}, bound: map[int]bool{}}
	for this.pos < len(this.tokens) {
		start := this.pos
		this.statement()
		if this.pos <= start {
			this.pos = start + 1
		}
	}

	this.dynamicImports()
	this.markExports()
	return this.file
}

func (this *parser) add(declaration *declaration, docLine int) {
	declaration.docLine = docLine
	declaration.refs = append(this.pending, declaration.refs...)
	this.pending = nil
	this.file.declarations = append(this.file.declarations, declaration)
}

// class reads a class declaration and its members.
func (this *parser) class(i int, prefix string, exported, isDefault bool, docLine int) int {
	start := i
	if this.get(i).is("abstract") {
		i++
	}

	i++
	name := ""
	if current := this.get(i); current.kind == tokenIdent && !current.is("extends") && !current.is("implements") {
		name = current.text
		i++
	}

	var typeParams, extends []token
	if this.get(i).is("<") {
		end := matchAngle(this.tokens, i)
		typeParams, i = this.tokens[i:end+1], end+1
	}

	var implements [][]token
	if this.get(i).is("extends") {
		from := i + 1
		i = heritageEnd(this.tokens, from, "implements")
		extends = this.tokens[from:i]
	}

	if this.get(i).is("implements") {
		from := i + 1
		i = heritageEnd(this.tokens, from, "")
		implements = splitTop(this.tokens[from:i], ",")
	}

	bodiless := i >= len(this.tokens)
	if !bodiless && !this.get(i).is("{") || (name == "" && !isDefault) {
		return statementEnd(this.tokens, start)
	}

	if name == "" {
		name = "default"
	}

	end := matching(this.tokens, i)
	result := &declaration{
		kind:       graph.KindType,
		name:       name,
		exported:   exported,
		signature:  collapse(prefix + spanText(this.src, this.tokens[start:i])),
		line:       this.tokens[start].line,
		endLine:    this.tokens[end].line,
		hashText:   spanText(this.src, this.tokens[start:end+1]),
		extends:    nonEmpty(extends),
		implements: implements,
		refs:       concat(typeParams, typeArguments(extends), typeArguments(implements...)),
	}

	result.key = result.signature
	this.export(result, isDefault)
	this.add(result, docLine)
	if !bodiless {
		this.members(result, this.tokens[i+1:end], false)
	}

	return end + 1
}

// commonJS reads module.exports and exports assignments at the top level.
func (this *parser) commonJS(docLine int) bool {
	start, i := this.pos, this.pos
	whole := false
	switch {
	case this.get(i).is("module") && this.get(i+1).is(".") && this.get(i+2).is("exports"):
		i += 3
		whole = true
	case this.get(i).is("exports"):
		i++
	default:
		return false
	}

	end := statementEnd(this.tokens, start)
	switch {
	case whole && this.get(i).is("="):
		value := trimSemicolon(this.tokens[i+1 : end])
		this.pos = i + 1
		switch {
		case len(value) == 1 && value[0].kind == tokenIdent:
			this.file.exports["default"] = value[0].text
		case len(value) > 0 && value[0].is("{"):
			this.exportObject(value[1:matching(value, 0)], docLine)
		case this.declaration("export default ", true, true, docLine):
			return true
		default:
			this.value("default", "export default", value, docLine)
		}
	case this.get(i).is(".") && this.get(i+1).kind == tokenIdent && this.get(i+2).is("="):
		name := this.get(i + 1).text
		value := trimSemicolon(this.tokens[i+3 : end])
		switch {
		case len(value) == 1 && value[0].kind == tokenIdent:
			this.file.exports[name] = value[0].text
		default:
			if parsed, ok := functionValue(this.src, value); ok {
				this.functionDeclaration(name, "exports."+name+" = "+parsed.text, parsed, this.tokens[start:end], docLine)
			} else {
				this.value(name, "exports."+name, value, docLine)
			}
		}
	default:
		return false
	}

	this.pos = end
	return true
}

// declaration reads a declaration at the current position, writing prefix,
// such as "export ", ahead of its signature.
func (this *parser) declaration(prefix string, exported, isDefault bool, docLine int) bool {
	i := this.pos
	if this.get(i).is("declare") {
		i++
	}

	current, next := this.get(i), this.get(i+1)
	switch {
	case current.is("function") || current.is("async") && next.is("function") && !next.newline:
		parsed, end := readFunction(this.src, this.tokens, i)
		name := parsed.name
		if name == "" && isDefault {
			name = "default"
		}

		if name != "" {
			result := this.functionDeclaration(name, prefix+parsed.text, parsed.function, this.tokens[i:end], docLine)
			result.exported = exported
			this.export(result, isDefault)
		}

		this.pos = end
	case current.is("class") || current.is("abstract") && next.is("class"):
		this.pos = this.class(i, prefix, exported, isDefault, docLine)
	case current.is("interface") && next.kind == tokenIdent:
		this.pos = this.iface(i, prefix, exported, isDefault, docLine)
	case current.is("type") && next.kind == tokenIdent && (this.get(i+2).is("=") || this.get(i+2).is("<")):
		end := statementEnd(this.tokens, i)
		whole := trimSemicolon(this.tokens[i:end])
		result := &declaration{kind: graph.KindType, name: next.text, exported: exported, signature: collapse(prefix + spanText(this.src, whole)), line: current.line, endLine: whole[len(whole)-1].line, hashText: spanText(this.src, whole), refs: whole[2:]}
		result.key = result.signature
		this.export(result, isDefault)
		this.add(result, docLine)
		this.pos = end
	case current.is("enum") || current.is("const") && next.is("enum"):
		this.pos = this.enum(i, prefix, exported, docLine)
	case (current.is("namespace") || current.is("module")) && next.kind == tokenIdent && !next.newline:
		end := statementEnd(this.tokens, i)
		header := i + 2
		for this.get(header).is(".") {
			header += 2
		}

		whole := this.tokens[i:end]
		result := &declaration{kind: graph.KindValue, name: next.text, exported: exported, signature: collapse(prefix + spanText(this.src, this.tokens[i:header])), line: current.line, endLine: whole[len(whole)-1].line, hashText: spanText(this.src, whole), refs: this.tokens[header:end]}
		result.key = result.signature
		this.export(result, isDefault)
		this.add(result, docLine)
		this.pos = end
	case current.is("const") || current.is("let") || current.is("var"):
		this.pos = this.variables(i, prefix, exported, docLine)
	case current.is("global") || (current.is("module") && next.kind == tokenString):
		this.pos = statementEnd(this.tokens, i)
	default:
		return false
	}

	return true
}

// decorators skips decorators ahead of a declaration, keeping their tokens
// as references of the declaration that follows.
func (this *parser) decorators() {
	for this.get(this.pos).is("@") {
		end := skipDecorator(this.tokens, this.pos)
		this.pending = append(this.pending, this.tokens[this.pos:end]...)
		this.pos = end
	}
}

// dynamicImports records every require("x") and import("x") call not
// already read as a binding: each is a dependency, used or not.
func (this *parser) dynamicImports() {
	for index := 0; index+3 < len(this.tokens); index++ {
		current := this.tokens[index]
		if !(current.is("require") || current.is("import")) || !this.tokens[index+1].is("(") || this.tokens[index+2].kind != tokenString || !this.tokens[index+3].is(")") {
			continue
		}

		if index > 0 && (this.tokens[index-1].is(".") || this.tokens[index-1].is("?.")) || this.bound[this.tokens[index+2].start] {
			continue
		}

		if path, ok := unquote(this.tokens[index+2]); ok {
			this.file.imports = append(this.file.imports, importSpec{path: path, line: current.line})
		}
	}
}

func (this *parser) enum(i int, prefix string, exported bool, docLine int) int {
	start := i
	if this.get(i).is("const") {
		i++
	}

	name := this.get(i + 1)
	open := i + 2
	bodiless := open >= len(this.tokens)
	if !bodiless && !this.get(open).is("{") {
		return statementEnd(this.tokens, start)
	}

	end := matching(this.tokens, open)
	result := &declaration{kind: graph.KindType, name: name.text, exported: exported, signature: collapse(prefix + spanText(this.src, this.tokens[start:open])), line: this.tokens[start].line, endLine: this.tokens[end].line, hashText: spanText(this.src, this.tokens[start:end+1]), refs: this.tokens[min(open+1, end):end]}
	result.key = result.signature
	for _, member := range splitTop(this.tokens[min(open+1, end):end], ",") {
		if len(member) > 0 {
			result.fields = append(result.fields, graph.Field{Text: collapse(spanText(this.src, member))})
			result.fieldPrivate = append(result.fieldPrivate, false)
		}
	}

	this.export(result, false)
	this.add(result, docLine)
	return end + 1
}

// export records an exported declaration under the name other files import
// it by.
func (this *parser) export(declaration *declaration, isDefault bool) {
	switch {
	case isDefault:
		this.file.exports["default"] = declaration.name
	case declaration.exported:
		this.file.exports[declaration.name] = declaration.name
	}
}

// exportObject reads module.exports = { ... }: shorthand and renamed
// properties export local names, and functions written inline become
// exported functions.
func (this *parser) exportObject(properties []token, docLine int) {
	for _, property := range splitTop(properties, ",") {
		switch {
		case len(property) == 0 || property[0].is("..."):
		case len(property) == 1 && property[0].kind == tokenIdent:
			this.file.exports[property[0].text] = property[0].text
		case len(property) == 3 && property[1].is(":") && property[2].kind == tokenIdent:
			this.file.exports[property[0].text] = property[2].text
		case property[0].kind == tokenIdent && property[1].is(":"):
			name, value := property[0].text, property[2:]
			if parsed, ok := functionValue(this.src, value); ok {
				this.functionDeclaration(name, "exports."+name+" = "+parsed.text, parsed, property, docLine)
			} else {
				this.value(name, "exports."+name, value, docLine)
			}
		case property[0].kind == tokenIdent && property[1].is("("):
			parsed, end := readSignature(property, 1)
			parsed.text = "function" + spanText(this.src, parsed.params)
			parsed.body = property[end:]
			this.functionDeclaration(property[0].text, "exports."+property[0].text+" = "+parsed.text, parsed, property, docLine)
		}
	}
}

func (this *parser) exportStatement(docLine int) {
	start := this.pos
	i := start + 1
	current := this.get(i)
	switch {
	case current.is("default"):
		this.pos = i + 1
		if this.declaration("export default ", true, true, docLine) {
			return
		}

		end := statementEnd(this.tokens, this.pos)
		value := trimSemicolon(this.tokens[this.pos:end])
		if len(value) == 1 && value[0].kind == tokenIdent {
			this.file.exports["default"] = value[0].text
		} else if parsed, ok := functionValue(this.src, value); ok {
			result := this.functionDeclaration("default", "export default "+parsed.text, parsed, this.tokens[start:end], docLine)
			this.export(result, true)
		} else {
			this.value("default", "export default", value, docLine)
		}

		this.pos = end
		return
	case current.is("*"):
		spec := importSpec{reexport: true}
		if this.get(i + 1).is("as") {
			spec.names = []binding{{local: this.get(i + 2).text, imported: "*"}}
			i += 3
		} else {
			spec.all = true
			i++
		}

		this.from(&spec, i)
	case current.is("{") || current.is("type") && this.get(i+1).is("{"):
		if current.is("type") {
			i++
		}

		end := matching(this.tokens, i)
		var names []binding
		for _, part := range splitTop(this.tokens[i+1:end], ",") {
			if name, as, ok := specifier(part); ok {
				names = append(names, binding{local: as, imported: name})
			}
		}

		if this.get(end + 1).is("from") {
			spec := importSpec{reexport: true, names: names}
			this.from(&spec, end+1)
		} else {
			for _, name := range names {
				this.file.exports[name.local] = name.imported
			}
		}
	case current.is("="):
		if value := trimSemicolon(this.tokens[i+1 : statementEnd(this.tokens, i)]); len(value) == 1 && value[0].kind == tokenIdent {
			this.file.exports["default"] = value[0].text
		}
	case current.is("import") || current.is("as"):
	default:
		this.pos = i
		if this.declaration("export ", true, false, docLine) {
			return
		}
	}

	this.pos = statementEnd(this.tokens, start)
}

// from reads the from "path" that ends an import or re-export at i and
// records the import.
func (this *parser) from(spec *importSpec, i int) {
	if !this.get(i).is("from") || this.get(i+1).kind != tokenString {
		return
	}

	path, ok := unquote(this.get(i + 1))
	if !ok {
		return
	}

	spec.path, spec.line = path, this.get(i+1).line
	this.bound[this.get(i+1).start] = true
	this.file.imports = append(this.file.imports, *spec)
}

// functionDeclaration records a function under name.
func (this *parser) functionDeclaration(name, signature string, parsed function, whole []token, docLine int) *declaration {
	result := &declaration{
		kind:      graph.KindFunction,
		name:      name,
		exported:  strings.HasPrefix(signature, "export") || strings.HasPrefix(signature, "exports."),
		signature: collapse(signature),
		key:       functionKey(this.src, name, parsed),
		line:      whole[0].line,
		endLine:   whole[len(whole)-1].line,
		hashText:  spanText(this.src, whole),
		refs:      concat(parsed.typeParams, parsed.params, parsed.result, parsed.body),
	}

	if strings.HasPrefix(signature, "exports.") {
		this.file.exports[name] = name
	}

	this.add(result, docLine)
	return result
}

func (this *parser) get(index int) token {
	if index >= 0 && index < len(this.tokens) {
		return this.tokens[index]
	}

	return token{kind: tokenOperator}
}

func (this *parser) iface(i int, prefix string, exported, isDefault bool, docLine int) int {
	start := i
	name := this.get(i + 1).text
	i += 2
	var typeParams []token
	if this.get(i).is("<") {
		end := matchAngle(this.tokens, i)
		typeParams, i = this.tokens[i:end+1], end+1
	}

	var extends [][]token
	if this.get(i).is("extends") {
		from := i + 1
		i = heritageEnd(this.tokens, from, "")
		extends = splitTop(this.tokens[from:i], ",")
	}

	bodiless := i >= len(this.tokens)
	if !bodiless && !this.get(i).is("{") {
		return statementEnd(this.tokens, start)
	}

	end := matching(this.tokens, i)
	result := &declaration{
		kind:      graph.KindInterface,
		name:      name,
		exported:  exported,
		signature: collapse(prefix + spanText(this.src, this.tokens[start:i])),
		line:      this.tokens[start].line,
		endLine:   this.tokens[end].line,
		hashText:  spanText(this.src, this.tokens[start:end+1]),
		extends:   extends,
		refs:      concat(typeParams, typeArguments(extends...)),
	}

	result.key = result.signature
	this.export(result, isDefault)
	this.add(result, docLine)
	if !bodiless {
		this.members(result, this.tokens[i+1:end], true)
	}

	return end + 1
}

func (this *parser) importStatement() {
	start := this.pos
	i := start + 1
	if next := this.get(i + 1); this.get(i).is("type") && (next.is("{") || next.is("*") || next.kind == tokenIdent && !next.is("from")) {
		i++
	}

	spec := importSpec{}
	if this.get(i).kind == tokenString {
		if path, ok := unquote(this.get(i)); ok {
			this.bound[this.get(i).start] = true
			this.file.imports = append(this.file.imports, importSpec{path: path, line: this.get(i).line})
		}

		this.pos = statementEnd(this.tokens, start)
		return
	}

	if current := this.get(i); current.kind == tokenIdent && !current.is("from") {
		if this.get(i + 1).is("=") {
			if this.get(i+2).is("require") && this.get(i+3).is("(") && this.get(i+4).kind == tokenString {
				if path, ok := unquote(this.get(i + 4)); ok {
					this.bound[this.get(i+4).start] = true
					this.file.imports = append(this.file.imports, importSpec{path: path, line: this.get(i + 4).line, names: []binding{{local: current.text, imported: "*"}}, require: true})
				}
			}

			this.pos = statementEnd(this.tokens, start)
			return
		}

		spec.names = append(spec.names, binding{local: current.text, imported: "default"})
		i++
		if this.get(i).is(",") {
			i++
		}
	}

	if this.get(i).is("*") && this.get(i+1).is("as") {
		spec.names = append(spec.names, binding{local: this.get(i + 2).text, imported: "*"})
		i += 3
	}

	if this.get(i).is("{") {
		end := matching(this.tokens, i)
		for _, part := range splitTop(this.tokens[i+1:end], ",") {
			if name, as, ok := specifier(part); ok {
				spec.names = append(spec.names, binding{local: as, imported: name})
			}
		}

		i = end + 1
	}

	this.from(&spec, i)
	this.pos = statementEnd(this.tokens, start)
}

// markExports marks the declarations an export list, export default name
// or module.exports names as exported, writing export ahead of their
// signatures so a signature alone says whether it is a contract.
func (this *parser) markExports() {
	exported := map[string]bool{}
	for _, local := range this.file.exports {
		exported[local] = true
	}

	for _, declaration := range this.file.declarations {
		if declaration.owner != nil || declaration.exported || !exported[declaration.name] {
			continue
		}

		declaration.exported = true
		declaration.signature = "export " + declaration.signature
		declaration.key = rekey(declaration)
	}
}

// members reads a class or interface body: methods become declarations of
// their own and everything else a field of the owner.
func (this *parser) members(owner *declaration, body []token, iface bool) {
	k := 0
	for k < len(body) {
		if body[k].is(";") || body[k].is(",") {
			k++
			continue
		}

		var decorators []token
		for k < len(body) && body[k].is("@") {
			end := skipDecorator(body, k)
			decorators = append(decorators, body[k:end]...)
			k = end
		}

		if k >= len(body) {
			break
		}

		if body[k].is("static") && k+1 < len(body) && body[k+1].is("{") {
			end := matching(body, k+1)
			owner.refs = append(owner.refs, body[k+1:end+1]...)
			k = end + 1
			continue
		}

		start := k
		private, async := false, false
		for k+1 < len(body) && body[k].kind == tokenIdent && memberModifier(body[k].text) && !nameEnds(body[k+1]) {
			private = private || body[k].is("private")
			async = async || body[k].is("async")
			k++
		}

		if k < len(body) && body[k].is("*") {
			k++
		}

		if k >= len(body) {
			break
		}

		name := ""
		switch current := body[k]; {
		case current.is("["):
			k = matching(body, k) + 1
		case current.is("new") && k+1 < len(body) && body[k+1].is("("):
		case current.kind == tokenIdent || current.kind == tokenNumber:
			name = current.text
			k++
		case current.kind == tokenString:
			name, _ = unquote(current)
			k++
		case current.is("(") || current.is("<"):
		default:
			k++
			continue
		}

		private = private || strings.HasPrefix(name, "#")
		if k < len(body) && (body[k].is("?") || body[k].is("!")) {
			k++
		}

		if name != "" && k < len(body) && (body[k].is("(") || body[k].is("<")) {
			parsed, end := readSignature(body, k)
			parsed.async = async
			sigEnd := end
			if end < len(body) && body[end].is("{") {
				close := matching(body, end)
				parsed.body = body[end : close+1]
				end = close + 1
			}

			method := &declaration{
				kind:      graph.KindMethod,
				name:      owner.name + "." + name,
				owner:     owner,
				private:   private,
				signature: collapse(spanText(this.src, body[start:sigEnd])),
				key:       functionKey(this.src, name, parsed),
				line:      body[start].line,
				endLine:   body[max(end-1, start)].line,
				hashText:  spanText(this.src, body[start:end]),
				refs:      concat(decorators, parsed.typeParams, parsed.params, parsed.result, parsed.body),
			}

			this.file.declarations = append(this.file.declarations, method)
			k = end
			continue
		}

		end := memberEnd(body, k)
		member := body[start:end]
		equals := untilTop(member, 0, "=")
		if !iface && name != "" && equals < len(member) {
			if parsed, ok := functionValue(this.src, member[equals+1:]); ok {
				method := &declaration{
					kind:      graph.KindMethod,
					name:      owner.name + "." + name,
					owner:     owner,
					private:   private,
					signature: collapse(spanText(this.src, member[:equals]) + " = " + parsed.text),
					key:       functionKey(this.src, name, parsed),
					line:      member[0].line,
					endLine:   member[len(member)-1].line,
					hashText:  spanText(this.src, member),
					refs:      concat(decorators, member[equals+1:]),
				}

				this.file.declarations = append(this.file.declarations, method)
				k = max(end, k+1)
				continue
			}
		}

		if text := collapse(spanText(this.src, member[:equals])); text != "" {
			owner.fields = append(owner.fields, graph.Field{Text: text})
			owner.fieldPrivate = append(owner.fieldPrivate, private)
		}

		owner.refs = append(owner.refs, decorators...)
		if offset := k - start; offset < len(member) {
			owner.refs = append(owner.refs, member[offset:]...)
		}

		k = max(end, k+1)
	}
}

func (this *parser) statement() {
	docLine := this.get(this.pos).line
	this.decorators()
	current := this.get(this.pos)
	switch {
	case current.is(";"):
		this.pos++
	case current.is("import") && !this.get(this.pos+1).is("(") && !this.get(this.pos+1).is("."):
		this.importStatement()
	case current.is("export"):
		this.exportStatement(docLine)
	case this.commonJS(docLine):
	case this.declaration("", false, false, docLine):
	default:
		this.pending = nil
		this.pos = statementEnd(this.tokens, this.pos)
	}
}

// value records a value declaration whose signature is head, followed by a
// short form of its value.
func (this *parser) value(name, head string, value []token, docLine int) {
	if len(value) == 0 {
		return
	}

	separator := " = "
	if head == "export default" {
		separator = " "
	}

	signature := head + separator + shortValue(collapse(spanText(this.src, value)))

	exported := strings.HasPrefix(head, "export") || strings.HasPrefix(head, "exports.")
	result := &declaration{kind: graph.KindValue, name: name, exported: exported, signature: collapse(signature), line: value[0].line, endLine: value[len(value)-1].line, hashText: spanText(this.src, value), refs: value}
	result.key = valueKey(result.signature)
	if strings.HasPrefix(head, "exports.") {
		this.file.exports[name] = name
	}

	if name == "default" {
		this.file.exports["default"] = name
	}

	this.add(result, docLine)
}

// variables reads const, let and var declarations. A function value makes
// a function; a require call makes an import, not a declaration.
func (this *parser) variables(i int, prefix string, exported bool, docLine int) int {
	keyword := this.get(i).text
	end := statementEnd(this.tokens, i)
	for _, part := range splitTop(trimSemicolon(this.tokens[i+1:end]), ",") {
		if len(part) == 0 {
			continue
		}

		equals := untilTop(part, 0, "=")
		head := part[:equals]
		var value []token
		if equals < len(part) {
			value = part[equals+1:]
		}

		colon := untilTop(head, 0, ":")
		pattern := head[:colon]
		if colon < len(head) && colon > 0 && head[colon-1].is("!") {
			pattern = head[:colon-1]
		}

		var typeTokens []token
		if colon < len(head) {
			typeTokens = head[colon+1:]
		}

		if this.require(pattern, value) {
			continue
		}

		if len(pattern) == 1 && pattern[0].kind == tokenIdent {
			name := pattern[0].text
			if parsed, ok := functionValue(this.src, value); ok {
				result := this.functionDeclaration(name, prefix+keyword+" "+name+" = "+parsed.text, parsed, part, docLine)
				result.exported = exported
				this.export(result, false)
				continue
			}

			signature := prefix + keyword + " " + name
			if len(typeTokens) > 0 {
				signature += ": " + spanText(this.src, typeTokens)
			}

			if keyword == "const" && len(value) > 0 {
				signature += " = " + shortValue(collapse(spanText(this.src, value)))
			}

			result := &declaration{kind: graph.KindValue, name: name, exported: exported, signature: collapse(signature), line: part[0].line, endLine: part[len(part)-1].line, hashText: spanText(this.src, part), refs: concat(typeTokens, value)}
			result.key = valueKey(result.signature)
			this.export(result, false)
			this.add(result, docLine)
			continue
		}

		for _, name := range bindingNames(pattern) {
			result := &declaration{kind: graph.KindValue, name: name.text, exported: exported, signature: collapse(prefix + keyword + " " + name.text), line: name.line, endLine: part[len(part)-1].line, hashText: spanText(this.src, part), refs: concat(typeTokens, value)}
			result.key = result.signature
			this.export(result, false)
			this.add(result, docLine)
		}
	}

	return end
}

// require reads const x = require("y"), const x = require("y").z and
// const { a, b: c } = require("y") as imports.
func (this *parser) require(pattern, value []token) bool {
	if len(value) < 4 || !value[0].is("require") || !value[1].is("(") || value[2].kind != tokenString || !value[3].is(")") {
		return false
	}

	path, ok := unquote(value[2])
	if !ok {
		return false
	}

	spec := importSpec{path: path, line: value[2].line, require: true}
	member := ""
	switch {
	case len(value) == 6 && value[4].is(".") && value[5].kind == tokenIdent:
		member = value[5].text
	case len(value) != 4:
		return false
	}

	switch {
	case len(pattern) == 1 && pattern[0].kind == tokenIdent && member != "":
		spec.names = []binding{{local: pattern[0].text, imported: member}}
	case len(pattern) == 1 && pattern[0].kind == tokenIdent:
		spec.names = []binding{{local: pattern[0].text, imported: "*"}}
	case len(pattern) > 1 && pattern[0].is("{") && member == "":
		for _, property := range splitTop(pattern[1:matching(pattern, 0)], ",") {
			switch {
			case len(property) == 1 && property[0].kind == tokenIdent:
				spec.names = append(spec.names, binding{local: property[0].text, imported: property[0].text})
			case len(property) == 3 && property[1].is(":") && property[2].kind == tokenIdent:
				spec.names = append(spec.names, binding{local: property[2].text, imported: property[0].text})
			}
		}
	}

	this.bound[value[2].start] = true
	this.file.imports = append(this.file.imports, spec)
	return true
}

// bindingNames lists the names a destructuring pattern declares.
func bindingNames(pattern []token) []token {
	if len(pattern) < 2 || !(pattern[0].is("{") || pattern[0].is("[")) {
		return nil
	}

	var result []token
	for _, element := range splitTop(pattern[1:matching(pattern, 0)], ",") {
		element = element[:untilTop(element, 0, "=")]
		if len(element) > 0 && element[0].is("...") {
			element = element[1:]
		}

		if colon := untilTop(element, 0, ":"); pattern[0].is("{") && colon < len(element) {
			element = element[colon+1:]
		}

		switch {
		case len(element) == 1 && element[0].kind == tokenIdent:
			result = append(result, element[0])
		case len(element) > 1:
			result = append(result, bindingNames(element)...)
		}
	}

	return result
}

func collapse(text string) string {
	text = whitespace.ReplaceAllString(strings.TrimSpace(text), " ")
	text = strings.ReplaceAll(text, "( ", "(")
	text = strings.ReplaceAll(text, " )", ")")
	text = strings.ReplaceAll(text, "< ", "<")
	text = strings.ReplaceAll(text, " >", ">")
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, " :", ":")
	text = strings.ReplaceAll(text, " ;", ";")
	return text
}

func concat(parts ...[]token) []token {
	var result []token
	for _, part := range parts {
		result = append(result, part...)
	}

	return result
}

// endsExpression reports whether a token can end an expression, so a line
// break after it may end the statement.
func endsExpression(current token) bool {
	switch current.kind {
	case tokenNumber, tokenString:
		return true
	case tokenIdent:
		switch current.text {
		case "extends", "implements", "as", "satisfies", "in", "instanceof", "of", "new", "typeof", "keyof", "void", "delete", "await", "yield", "is", "return", "export", "import", "default", "async", "declare", "abstract", "readonly", "static", "public", "private", "protected", "const", "let", "var", "function", "class", "interface", "type", "enum", "namespace", "module":
			return false
		}

		return true
	default:
		switch current.text {
		case ")", "]", "}", ">", "++", "--", "!":
			return true
		}

		return false
	}
}

// functionKey is the comparison key of a function: its name, type
// parameters, parameter types and result, with parameter names dropped.
func functionKey(src []byte, name string, parsed function) string {
	result := name + collapse(spanText(src, parsed.typeParams)) + "(" + strings.Join(paramKeys(src, parsed.params), ",") + ")"
	if len(parsed.result) > 0 {
		result += ":" + collapse(spanText(src, parsed.result))
	}

	if parsed.async {
		result = "async " + result
	}

	return result
}

// functionValue reads a function expression or arrow function, as the
// value of a variable, property or default export.
func functionValue(src []byte, value []token) (result function, ok bool) {
	if len(value) == 0 {
		return result, false
	}

	k := 0
	if value[0].is("async") && len(value) > 1 && !value[1].newline && (value[1].is("(") || value[1].is("function") || value[1].kind == tokenIdent) {
		result.async = true
		k++
	}

	if k < len(value) && value[k].is("function") {
		parsed, end := readFunction(src, value, k)
		if parsed.params == nil {
			return result, false
		}

		parsed.async = result.async
		parsed.text = collapse(spanText(src, value[:parsed.sigEnd]))
		parsed.body = value[min(end, len(value)):]
		return parsed.function, true
	}

	if k < len(value) && value[k].kind == tokenIdent && !isKeyword(value[k].text) {
		if k+1 >= len(value) || !value[k+1].is("=>") {
			return result, false
		}

		result.params = value[k : k+1]
		k++
	} else {
		if k < len(value) && value[k].is("<") {
			end := matchAngle(value, k)
			result.typeParams, k = value[k:end+1], end+1
		}

		if k >= len(value) || !value[k].is("(") {
			return result, false
		}

		end := matching(value, k)
		result.params, k = value[k:end+1], end+1
		if k < len(value) && value[k].is(":") {
			end := typeEnd(value, k+1, true)
			result.result, k = value[k+1:end], end
		}

		if k >= len(value) || !value[k].is("=>") {
			return result, false
		}
	}

	result.text = collapse(spanText(src, value[:k+1]))
	result.body = value[k+1:]
	return result, true
}

// heritageEnd finds where an extends or implements list ends: at the class
// or interface body, or at stop.
func heritageEnd(tokens []token, from int, stop string) int {
	angle := 0
	for index := from; index < len(tokens); index++ {
		current := tokens[index]
		switch {
		case current.is("(") || current.is("["):
			index = matching(tokens, index)
		case current.is("<"):
			angle++
		case current.is(">"):
			angle--
		case current.is("{") && angle > 0:
			index = matching(tokens, index)
		case current.is("{"), stop != "" && current.is(stop) && angle == 0:
			return index
		}
	}

	return len(tokens)
}

// matchAngle returns the index of the angle bracket closing the one at
// open.
func matchAngle(tokens []token, open int) int {
	depth := 0
	for index := open; index < len(tokens); index++ {
		switch current := tokens[index]; {
		case current.is("(") || current.is("[") || current.is("{"):
			index = matching(tokens, index)
		case current.is("<"):
			depth++
		case current.is(">"):
			depth--
			if depth == 0 {
				return index
			}
		}
	}

	return len(tokens) - 1
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

// memberEnd finds where a class field or interface member ends.
func memberEnd(tokens []token, from int) int {
	angle := 0
	for index := from; index < len(tokens); index++ {
		current := tokens[index]
		if index > from && current.newline && endsExpression(tokens[index-1]) && startsMember(current) {
			return index
		}

		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			index = matching(tokens, index)
		case current.is("<"):
			angle++
		case current.is(">"):
			angle = max(angle-1, 0)
		case current.is(";"), current.is(",") && angle == 0:
			return index
		}
	}

	return len(tokens)
}

func memberModifier(text string) bool {
	switch text {
	case "public", "private", "protected", "static", "readonly", "abstract", "override", "declare", "async", "accessor", "get", "set":
		return true
	}

	return false
}

// nameEnds reports whether a token after a modifier word shows that the
// word is itself the member's name, as in a method named get().
func nameEnds(next token) bool {
	if next.newline {
		return true
	}

	switch next.text {
	case "(", "=", ":", ";", "?", "!", "<", ",", "}":
		return next.kind == tokenOperator
	}

	return false
}

func nonEmpty(tokens []token) [][]token {
	if len(tokens) == 0 {
		return nil
	}

	return [][]token{tokens}
}

// paramKeys lists each parameter's comparison key: its type, or _ when it
// has none, marked ... when it is a rest parameter and ? when optional.
func paramKeys(src []byte, params []token) []string {
	if len(params) == 0 {
		return nil
	}

	if !params[0].is("(") {
		return []string{"_"}
	}

	var result []string
	for _, part := range splitTop(params[1:matching(params, 0)], ",") {
		k := 0
		for k < len(part) && part[k].is("@") {
			k = skipDecorator(part, k)
		}

		for k+1 < len(part) && part[k].kind == tokenIdent && memberModifier(part[k].text) && !nameEnds(part[k+1]) {
			k++
		}

		if k >= len(part) {
			continue
		}

		key := ""
		if part[k].is("...") {
			key = "..."
			k++
		}

		if k < len(part) && (part[k].is("{") || part[k].is("[")) {
			k = matching(part, k) + 1
		} else {
			k++
		}

		optional := false
		if k < len(part) && part[k].is("?") {
			optional = true
			k++
		}

		typeText := "_"
		if k < len(part) && part[k].is(":") {
			end := untilTop(part, k+1, "=")
			typeText = collapse(spanText(src, part[k+1:end]))
			k = end
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

// namedFunction is a function declaration's parse: its name, shape and
// where its signature ends.
type namedFunction struct {
	function
	name   string
	sigEnd int
}

// readFunction reads a function declaration or expression starting at the
// async or function keyword.
func readFunction(src []byte, tokens []token, i int) (result namedFunction, end int) {
	start := i
	if i < len(tokens) && tokens[i].is("async") {
		result.async = true
		i++
	}

	i++
	if i < len(tokens) && tokens[i].is("*") {
		i++
	}

	if i < len(tokens) && tokens[i].kind == tokenIdent {
		result.name = tokens[i].text
		i++
	}

	if i < len(tokens) && tokens[i].is("<") {
		close := matchAngle(tokens, i)
		result.typeParams, i = tokens[i:close+1], close+1
	}

	if i >= len(tokens) || !tokens[i].is("(") {
		return namedFunction{}, statementEnd(tokens, start)
	}

	close := matching(tokens, i)
	result.params, i = tokens[i:close+1], close+1
	if i < len(tokens) && tokens[i].is(":") {
		close := typeEnd(tokens, i+1, false)
		result.result, i = tokens[i+1:close], close
	}

	result.sigEnd = i
	result.text = collapse(spanText(src, tokens[start:i]))
	switch {
	case i < len(tokens) && tokens[i].is("{"):
		close := matching(tokens, i)
		result.body, i = tokens[i:close+1], close+1
	case i < len(tokens) && tokens[i].is(";"):
		i++
	}

	return result, i
}

// readSignature reads a method's type parameters, parameters and result
// type, starting at the type parameters or the opening parenthesis.
func readSignature(tokens []token, i int) (result function, end int) {
	if tokens[i].is("<") {
		close := matchAngle(tokens, i)
		result.typeParams, i = tokens[i:close+1], close+1
	}

	if i >= len(tokens) || !tokens[i].is("(") {
		return result, i
	}

	close := matching(tokens, i)
	result.params, i = tokens[i:close+1], close+1
	if i < len(tokens) && tokens[i].is(":") {
		close := typeEnd(tokens, i+1, false)
		result.result, i = tokens[i+1:close], close
	}

	return result, i
}

// rekey recomputes a declaration's comparison key after its signature
// changed.
func rekey(declaration *declaration) string {
	switch declaration.kind {
	case graph.KindFunction, graph.KindMethod:
		return declaration.key
	case graph.KindValue:
		return valueKey(declaration.signature)
	default:
		return declaration.signature
	}
}

func shortValue(text string) string {
	if len(text) > 60 || strings.ContainsAny(text, "\n\r") {
		return "…"
	}

	return text
}

func skipDecorator(tokens []token, k int) int {
	k++
	if k < len(tokens) && tokens[k].kind == tokenIdent {
		k++
	}

	for k+1 < len(tokens) && tokens[k].is(".") && tokens[k+1].kind == tokenIdent {
		k += 2
	}

	if k < len(tokens) && tokens[k].is("(") {
		k = matching(tokens, k) + 1
	}

	return k
}

// spanText renders tokens as source text with comments removed and every
// gap reduced to one space.
func spanText(src []byte, tokens []token) string {
	var builder strings.Builder
	previous := -1
	for _, current := range tokens {
		if current.end <= current.start || current.end > len(src) {
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

// specifier reads one import or export specifier: a, a as b, or type a.
func specifier(part []token) (name, as string, ok bool) {
	if len(part) > 1 && part[0].is("type") {
		part = part[1:]
	}

	if len(part) == 0 {
		return "", "", false
	}

	name = part[0].text
	if part[0].kind == tokenString {
		name, _ = unquote(part[0])
	}

	as = name
	if len(part) >= 3 && part[1].is("as") {
		as = part[2].text
	}

	return name, as, true
}

// splitTop splits tokens at a separator that is not nested in brackets or
// type arguments.
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

// startsMember reports whether a token on a new line can begin a class or
// interface member.
func startsMember(current token) bool {
	return startsStatement(current) || current.is("[") || current.kind == tokenString || current.is("*")
}

// startsStatement reports whether a token on a new line begins a new
// statement rather than continuing the last one.
func startsStatement(current token) bool {
	switch current.kind {
	case tokenIdent:
		switch current.text {
		case "as", "satisfies", "in", "instanceof", "of", "extends", "implements", "is", "keyof":
			return false
		}

		return true
	case tokenOperator:
		return current.text == "@"
	default:
		return false
	}
}

// statementEnd finds where a top-level statement ends: after a semicolon,
// or before a line that starts a new statement when the last line could
// end one.
func statementEnd(tokens []token, from int) int {
	for index := from; index < len(tokens); index++ {
		current := tokens[index]
		if index > from && current.newline && endsExpression(tokens[index-1]) && startsStatement(current) {
			return index
		}

		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			index = matching(tokens, index)
		case current.is(";"):
			return index + 1
		}
	}

	return len(tokens)
}

func trimSemicolon(tokens []token) []token {
	if len(tokens) > 0 && tokens[len(tokens)-1].is(";") {
		return tokens[:len(tokens)-1]
	}

	return tokens
}

// typeArguments keeps the type arguments of extends and implements
// clauses, such as Foo in Base<Foo>, as references; the named types
// themselves become embeds and implements edges instead.
func typeArguments(clauses ...[]token) []token {
	var result []token
	for _, clause := range clauses {
		for index, current := range clause {
			if current.is("<") {
				result = append(result, clause[index:]...)
				break
			}
		}
	}

	return result
}

// typeEnd finds where a type annotation ends. A brace starts an object type
// only where a type is expected, so a function body is not read as one; in
// an arrow function's result type, => ends the type.
func typeEnd(tokens []token, from int, arrow bool) int {
	angle := 0
	for index := from; index < len(tokens); index++ {
		current := tokens[index]
		if index > from && angle == 0 && current.newline && endsExpression(tokens[index-1]) && startsStatement(current) {
			return index
		}

		switch {
		case current.is("(") || current.is("["):
			index = matching(tokens, index)
		case current.is("{"):
			if index == from || angle > 0 || typeOperand(tokens[index-1]) {
				index = matching(tokens, index)
				continue
			}

			return index
		case current.is("<"):
			angle++
		case current.is(">"):
			if angle == 0 {
				return index
			}

			angle--
		case angle == 0 && arrow && current.is("=>"):
			return index
		case angle == 0 && (current.is(";") || current.is("=") || current.is(",") || current.is(")") || current.is("]") || current.is("}")):
			return index
		}
	}

	return len(tokens)
}

// typeOperand reports whether a token leaves a type expecting an operand,
// so a brace after it opens an object type.
func typeOperand(previous token) bool {
	if previous.kind == tokenIdent {
		return previous.is("keyof") || previous.is("typeof") || previous.is("readonly") || previous.is("extends") || previous.is("is")
	}

	return previous.kind == tokenOperator && !previous.is(")") && !previous.is("]") && !previous.is(">") && !previous.is("}")
}

func unquote(current token) (string, bool) {
	text := current.text
	if len(text) < 2 || current.kind != tokenString || strings.Contains(text, "${") {
		return "", false
	}

	quote := text[0]
	if (quote != '"' && quote != '\'' && quote != '`') || text[len(text)-1] != quote {
		return "", false
	}

	return text[1 : len(text)-1], true
}

// untilTop returns the index of the first separator outside brackets and
// type arguments, or len(tokens).
func untilTop(tokens []token, pos int, separator string) int {
	angle := 0
	for pos < len(tokens) {
		current := tokens[pos]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			pos = matching(tokens, pos) + 1
			continue
		case current.is("<"):
			angle++
		case current.is(">"):
			angle = max(angle-1, 0)
		case current.text == separator && current.kind == tokenOperator && (angle == 0 || separator == ";"):
			return pos
		}

		pos++
	}

	return pos
}

// valueKey is a value's comparison key: its signature without its value,
// since a changed value keeps callers working.
func valueKey(signature string) string {
	if strings.HasPrefix(signature, "export default ") {
		return "export default"
	}

	head, _, _ := strings.Cut(signature, " = ")
	return head
}
