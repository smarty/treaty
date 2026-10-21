package csharp

import (
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

// follows lists the tokens after a > that keep a < … > as type arguments
// in an expression, as the C# grammar decides.
var follows = map[string]bool{
	"(": true, ")": true, "]": true, "}": true, ":": true, ";": true, ",": true, ".": true, "?": true,
	"==": true, "!=": true, "|": true, "^": true, "&&": true, "||": true, "&": true, "[": true,
}

// declarationStarts lists the tokens after which a name starts a type in a
// statement, such as List<Line> lines = …, so its type arguments may be
// followed by a name.
var declarationStarts = map[string]bool{
	";": true, "{": true, "}": true, "new": true, "is": true, "as": true, "typeof": true,
	"out": true, "ref": true, "default": true, "nameof": true, "sizeof": true, "const": true,
}

// resolver holds the tables that name lookup uses.
type resolver struct {
	g          *graph.Graph
	built      built
	projects   map[string]*project
	types      map[string][]string
	namespaces map[string]bool
	modules    map[string][]string
	globals    map[*project][]usingDirective
	members    map[string][]*graph.Symbol
	methods    map[string][]*graph.Symbol
	parameters map[string][]parameter
	bases      map[string][]string
}

// segment is one name of a dotted chain, with its type argument count and
// where it is.
type segment struct {
	name  string
	arity int
	at    int
}

// scope is where a piece of code looks names up.
type scope struct {
	state      *fileState
	owners     []string
	namespaces []string
	closure    map[*project]bool
}

// references records the edges and imports of every declaration.
func references(states []*fileState, g *graph.Graph, made built, projects *projectSet) {
	this := &resolver{
		g: g, built: made, projects: map[string]*project{}, types: map[string][]string{}, namespaces: map[string]bool{},
		modules: map[string][]string{}, globals: map[*project][]usingDirective{}, members: map[string][]*graph.Symbol{},
		methods: map[string][]*graph.Symbol{}, parameters: map[string][]parameter{}, bases: map[string][]string{},
	}

	for _, state := range states {
		this.projects[state.module] = state.project
		for _, using := range state.usings {
			if using.global {
				this.globals[state.project] = append(this.globals[state.project], using)
			}
		}

		for _, declaration := range state.declarations {
			id := made.ids[declaration]
			if this.parameters[id] == nil {
				this.parameters[id] = declaration.parameters
			}

			if declaration.owner != nil || (declaration.kind != graph.KindType && declaration.kind != graph.KindInterface) {
				continue
			}

			for at := declaration.namespace; ; at = parentNamespace(at) {
				this.namespaces[at] = true
				if at == "" {
					break
				}
			}

			key := declaration.namespace + "\x00" + declaration.simple + arity(len(declaration.typeParams))
			if !slices.Contains(this.types[key], id) {
				this.types[key] = append(this.types[key], id)
			}

			module := g.Symbol(id).Module
			if !slices.Contains(this.modules[declaration.namespace], module) {
				this.modules[declaration.namespace] = append(this.modules[declaration.namespace], module)
			}
		}
	}

	for _, each := range projects.byPath {
		for _, using := range each.usings {
			this.globals[each] = append(this.globals[each], using)
		}
	}

	for _, symbol := range made.order {
		if symbol.Parent != "" {
			owner := graph.SymbolID(symbol.Module, symbol.Parent)
			this.members[owner] = append(this.members[owner], symbol)
		}

		if symbol.Kind == graph.KindMethod {
			this.methods[memberName(symbol)] = append(this.methods[memberName(symbol)], symbol)
		}
	}

	for _, state := range states {
		for _, declaration := range state.declarations {
			if len(declaration.bases) == 0 {
				continue
			}

			id := made.ids[declaration]
			where := this.scopeOf(state, declaration)
			for _, base := range declaration.bases {
				target := this.typeOf(where, base)
				if target == "" {
					continue
				}

				this.bases[id] = append(this.bases[id], target)
				kind := graph.EdgeEmbeds
				if other := g.Symbol(target); other != nil && other.Kind == graph.KindInterface {
					kind = graph.EdgeImplements
				}

				this.edge(id, target, kind, state, base[0])
			}
		}
	}

	for _, state := range states {
		this.imports(state)
		for _, declaration := range state.declarations {
			id := made.ids[declaration]
			where := this.scopeOf(state, declaration)
			emit := func(target, kind string, at token) { this.edge(id, target, kind, state, at) }
			this.scan(where, declaration.header, true, emit)
			for _, base := range declaration.bases {
				this.scan(where, base, true, emit)
			}

			this.scan(where, declaration.body, false, emit)
			this.scan(where, declaration.hidden, false, emit)
		}
	}
}

// edge records a reference, unless it points at its own symbol or at the
// type that holds the symbol.
func (this *resolver) edge(from, to, kind string, state *fileState, at token) {
	source, target := this.g.Symbol(from), this.g.Symbol(to)
	if source == nil || target == nil || from == to || (source.Parent != "" && target.Module == source.Module && target.Name == source.Parent) {
		return
	}

	this.g.AddEdge(graph.Edge{From: from, To: to, Kind: kind, File: state.path, Line: at.line})
}

// imports records the module an alias or a using static names.
func (this *resolver) imports(state *fileState) {
	for _, using := range state.usings {
		if using.alias == "" && !using.static {
			continue
		}

		target := this.typeOf(this.fileScope(state, using.namespace), using.tokens)
		if symbol := this.g.Symbol(target); symbol != nil && symbol.Module != state.module {
			this.g.AddImport(graph.Import{From: state.module, To: symbol.Module, File: state.path, Line: using.line})
		}
	}
}

// scopeOf builds the scope of a declaration: its types from the inside out
// and its namespaces from the inside out.
func (this *resolver) scopeOf(state *fileState, declaration *declaration) scope {
	result := this.fileScope(state, declaration.namespace)
	for at := declaration; at != nil; at = at.owner {
		if at.kind == graph.KindType || at.kind == graph.KindInterface {
			result.owners = append(result.owners, this.built.ids[at])
		}
	}

	return result
}

// fileScope builds the scope of code at a namespace level of a file.
func (this *resolver) fileScope(state *fileState, namespace string) scope {
	result := scope{state: state, closure: map[*project]bool{}}
	for each := range this.closureOf(state.project) {
		result.closure[each] = true
	}

	for at := namespace; ; at = parentNamespace(at) {
		result.namespaces = append(result.namespaces, at)
		if at == "" {
			break
		}
	}

	return result
}

func (this *resolver) closureOf(start *project) map[*project]bool {
	result := map[*project]bool{}
	var visit func(*project)
	visit = func(each *project) {
		if each == nil || result[each] {
			return
		}

		result[each] = true
		for _, reference := range each.references {
			for _, other := range this.projects {
				if other.path == reference {
					visit(other)
				}
			}
		}
	}

	visit(start)
	return result
}

// usingsAt lists the using directives a scope sees at one namespace level.
func (this *resolver) usingsAt(where scope, level string) []usingDirective {
	var result []usingDirective
	for _, using := range where.state.usings {
		if using.namespace == level && !using.global {
			result = append(result, using)
		}
	}

	if level == "" {
		result = append(result, this.globals[where.state.project]...)
	}

	return result
}

// visible keeps the types a scope's project can use.
func (this *resolver) visible(where scope, ids []string) []string {
	var result []string
	for _, id := range ids {
		if symbol := this.g.Symbol(id); symbol != nil && where.closure[this.projects[symbol.Module]] && !slices.Contains(result, id) {
			result = append(result, id)
		}
	}

	return result
}

// simpleType finds the type a simple name means in a scope: a containing
// type or a type nested in one, then at each namespace level the
// namespace's types, its aliases, and the types its usings bring in.
func (this *resolver) simpleType(where scope, name string, count int) string {
	key := name + arity(count)
	for _, owner := range where.owners {
		symbol := this.g.Symbol(owner)
		if symbol == nil {
			continue
		}

		if simpleName(symbol.Name) == key {
			return owner
		}

		if nested := this.g.Symbol(graph.SymbolID(symbol.Module, symbol.Name+"."+key)); nested != nil {
			return nested.ID
		}

		for _, base := range this.bases[owner] {
			if other := this.g.Symbol(base); other != nil {
				if nested := this.g.Symbol(graph.SymbolID(other.Module, other.Name+"."+key)); nested != nil {
					return nested.ID
				}
			}
		}
	}

	for _, level := range where.namespaces {
		if found := this.visible(where, this.types[level+"\x00"+key]); len(found) == 1 {
			return found[0]
		}

		usings := this.usingsAt(where, level)
		for _, using := range usings {
			if using.alias == name && count == 0 {
				return this.typeOf(this.fileScope(where.state, ""), using.tokens)
			}
		}

		var found []string
		for _, using := range usings {
			if using.alias == "" && !using.static {
				found = append(found, this.visible(where, this.types[using.target+"\x00"+key])...)
			}
		}

		if found = this.visible(where, found); len(found) == 1 {
			return found[0]
		}

		if len(found) > 1 {
			return ""
		}
	}

	return ""
}

// qualified resolves the longest start of a chain that names a type: a
// namespace and a type, or a simple type, then nested types.
//
// Returns:
//   - id: the type, or empty.
//   - used: how many segments the type took.
func (this *resolver) qualified(where scope, chain []segment) (id string, used int) {
	for split := len(chain) - 1; split >= 1; split-- {
		var parts []string
		for _, each := range chain[:split] {
			parts = append(parts, each.name)
		}

		prefix := strings.Join(parts, ".")
		for _, level := range where.namespaces {
			namespace := strings.Trim(level+"."+prefix, ".")
			if !this.namespaces[namespace] {
				continue
			}

			found := this.visible(where, this.types[namespace+"\x00"+chain[split].name+arity(chain[split].arity)])
			if len(found) == 1 {
				id, used = this.nested(found[0], chain, split+1)
				return id, used
			}
		}
	}

	if id = this.simpleType(where, chain[0].name, chain[0].arity); id == "" {
		return "", 0
	}

	return this.nested(id, chain, 1)
}

// nested follows nested types along a chain from a type.
func (this *resolver) nested(id string, chain []segment, from int) (string, int) {
	for from < len(chain) {
		symbol := this.g.Symbol(id)
		next := this.g.Symbol(graph.SymbolID(symbol.Module, symbol.Name+"."+chain[from].name+arity(chain[from].arity)))
		if next == nil || (next.Kind != graph.KindType && next.Kind != graph.KindInterface) {
			break
		}

		id = next.ID
		from++
	}

	return id, from
}

// typeOf resolves the type a token list starts with, as in a base list or
// a using directive.
func (this *resolver) typeOf(where scope, tokens []token) string {
	chain, _, _ := chainAt(tokens, 0, true)
	if len(chain) == 0 {
		return ""
	}

	id, used := this.qualified(where, chain)
	if used != len(chain) {
		return ""
	}

	return id
}

// scan records an edge for each name in tokens that resolves. In a
// signature, only types resolve; in a body, members and calls do too.
func (this *resolver) scan(where scope, tokens []token, signature bool, emit func(target, kind string, at token)) {
	for index := 0; index < len(tokens); index++ {
		current := tokens[index]
		if current.kind != tokenIdent {
			continue
		}

		previous := ""
		if index > 0 {
			previous = tokens[index-1].text
		}

		if previous == "." || previous == "::" || previous == "->" || previous == "?." {
			if !signature && at(tokens, index+1).is("(") && !reserved[current.text] {
				emit(this.uniqueMethod(where, current.text, arguments(tokens, index+1)), graph.EdgeCall, current)
			}

			continue
		}

		if current.is("global") && at(tokens, index+1).is("::") {
			continue
		}

		if reserved[current.text] && !current.is("this") && !current.is("base") {
			continue
		}

		loose := signature || index == 0 || declarationStarts[previous]
		chain, last, after := chainAt(tokens, index, loose)
		if len(chain) == 0 {
			continue
		}

		call := at(tokens, after).is("(")
		count := 0
		if call {
			count = arguments(tokens, after)
		}

		if signature || previous == "new" {
			if id, _ := this.qualified(where, chain); id != "" {
				emit(id, graph.EdgeTypeUse, current)
			}

			index = last
			continue
		}

		this.chain(where, chain, call, count, emit, current)
		index = last
	}
}

// chain resolves one dotted chain in a body.
func (this *resolver) chain(where scope, chain []segment, call bool, count int, emit func(target, kind string, at token), current token) {
	first := chain[0]
	if first.name == "this" || first.name == "base" {
		if len(chain) > 1 && len(where.owners) > 0 {
			this.member(where.owners[0], chain[1].name, len(chain) == 2 && call, count, emit, current)
		}

		return
	}

	if len(chain) == 1 {
		for _, owner := range where.owners {
			if this.member(owner, first.name, call, count, emit, current) {
				return
			}
		}

		for _, level := range where.namespaces {
			for _, using := range this.usingsAt(where, level) {
				if using.static {
					if owner := this.typeOf(this.fileScope(where.state, ""), using.tokens); owner != "" && this.member(owner, first.name, call, count, emit, current) {
						return
					}
				}
			}
		}
	}

	id, used := this.qualified(where, chain)
	switch {
	case id != "" && used == len(chain):
		emit(id, graph.EdgeTypeUse, current)
	case id != "":
		rest := chain[used:]
		if !this.member(id, rest[0].name, len(rest) == 1 && call, count, emit, current) {
			emit(id, graph.EdgeTypeUse, current)
		}
	case len(chain) > 1 && call:
		emit(this.uniqueMethod(where, chain[len(chain)-1].name, count), graph.EdgeCall, current)
	}
}

// member records an edge to a member of a type, or of its bases, by name.
// A call picks the overload that accepts the argument count; when none or
// several do, the edge goes to the type.
//
// Returns:
//   - result: true when the type, or a base, has a member of that name.
func (this *resolver) member(owner, name string, call bool, count int, emit func(target, kind string, at token), current token) bool {
	seen := map[string]bool{}
	for queue := []string{owner}; len(queue) > 0; queue = queue[1:] {
		id := queue[0]
		if seen[id] {
			continue
		}

		seen[id] = true
		var methods, others []*graph.Symbol
		constructor := strings.TrimRight(simpleName(this.g.Symbol(id).Name), "<,>")
		for _, each := range this.members[id] {
			if memberName(each) != name || name == constructor {
				continue
			}

			if each.Kind == graph.KindMethod {
				methods = append(methods, each)
			} else {
				others = append(others, each)
			}
		}

		switch {
		case call && len(methods) > 0:
			var fitting []string
			for _, each := range methods {
				if fits(this.parameters[each.ID], count, false) {
					fitting = append(fitting, each.ID)
				}
			}

			if len(fitting) == 1 {
				emit(fitting[0], graph.EdgeCall, current)
			} else {
				emit(id, graph.EdgeTypeUse, current)
			}

			return true
		case len(others) > 0:
			emit(others[0].ID, graph.EdgeTypeUse, current)
			return true
		case len(methods) > 0:
			emit(id, graph.EdgeTypeUse, current)
			return true
		}

		queue = append(queue, this.bases[id]...)
	}

	return false
}

// uniqueMethod finds the one method of a name that accepts the argument
// count among the modules a scope reaches: its own and those of its
// namespaces in scope. An extension method's this parameter does not count.
func (this *resolver) uniqueMethod(where scope, name string, count int) string {
	reach := map[string]bool{where.state.module: true}
	for _, level := range where.namespaces {
		for _, module := range this.modules[level] {
			reach[module] = true
		}

		for _, using := range this.usingsAt(where, level) {
			if using.alias == "" && !using.static {
				for _, module := range this.modules[using.target] {
					reach[module] = true
				}
			}
		}
	}

	var found []string
	for _, candidate := range this.methods[name] {
		if !reach[candidate.Module] || !where.closure[this.projects[candidate.Module]] {
			continue
		}

		if fits(this.parameters[candidate.ID], count, false) || fits(this.parameters[candidate.ID], count, true) {
			found = append(found, candidate.ID)
		}
	}

	if len(found) != 1 {
		return ""
	}

	return found[0]
}

// fits reports whether a parameter list accepts an argument count. For an
// extension call, the this parameter is the receiver.
func fits(parameters []parameter, count int, extension bool) bool {
	if extension {
		if len(parameters) == 0 || !parameters[0].this {
			return false
		}

		parameters = parameters[1:]
	}

	required, rest := 0, false
	for _, each := range parameters {
		switch {
		case each.params:
			rest = true
		case !each.optional:
			required++
		}
	}

	return count >= required && (count <= len(parameters) || rest)
}

// chainAt reads a dotted chain of names from index, with their type
// argument counts. In loose mode, a < after a name always opens type
// arguments when a matching > closes them.
//
// Returns:
//   - chain: the names.
//   - last: the index of the last name.
//   - after: the index after the chain and its type arguments.
func chainAt(tokens []token, index int, loose bool) (chain []segment, last, after int) {
	for index < len(tokens) && tokens[index].kind == tokenIdent {
		if at(tokens, index+1).is("::") {
			index += 2
			continue
		}

		each := segment{name: identifier(tokens[index].text), at: index}
		last = index
		index++
		if at(tokens, index).is("<") {
			if end, count, ok := typeArguments(tokens, index, loose); ok {
				each.arity, index = count, end
			}
		}

		chain = append(chain, each)
		if !at(tokens, index).is(".") || at(tokens, index+1).kind != tokenIdent {
			break
		}

		index++
	}

	return chain, last, index
}

// typeArguments reads a type argument list that opens at index.
//
// Returns:
//   - end: the index after the closing >.
//   - count: the number of type arguments.
//   - ok: the tokens form a type argument list here.
func typeArguments(tokens []token, index int, loose bool) (end, count int, ok bool) {
	depth, count := 0, 1
	for at := index; at < len(tokens); at++ {
		current := tokens[at]
		switch {
		case current.is("<"):
			depth++
		case current.is(">"):
			depth--
			if depth == 0 {
				next := ""
				if at+1 < len(tokens) {
					next = tokens[at+1].text
				}

				if loose || follows[next] || at+1 >= len(tokens) {
					return at + 1, count, true
				}

				return 0, 0, false
			}
		case current.is(",") && depth == 1:
			count++
		case current.kind == tokenIdent || current.is(".") || current.is(",") || current.is("?") || current.is("[") || current.is("]") || current.is("(") || current.is(")") || current.is("::") || current.is("*"):
		default:
			return 0, 0, false
		}
	}

	return 0, 0, false
}

// arguments counts the arguments of the call whose ( is at index.
func arguments(tokens []token, index int) int {
	depth, count, empty := 0, 1, true
	for at := index; at < len(tokens); at++ {
		current := tokens[at]
		if current.kind == tokenIdent && at+1 < len(tokens) && tokens[at+1].is("<") {
			previous := tokens[at-1].text
			if end, _, ok := typeArguments(tokens, at+1, declarationStarts[previous]); ok {
				at = end - 1
				empty = false
				continue
			}
		}

		if current.kind != tokenOperator {
			empty = false
			continue
		}

		switch current.text {
		case "(", "[", "{":
			depth++
			if depth > 1 {
				empty = false
			}
		case ")", "]", "}":
			depth--
			if depth == 0 {
				if empty {
					return 0
				}

				return count
			}
		case ",":
			if depth == 1 {
				count++
			}
		default:
			empty = false
		}
	}

	return count
}

func at(tokens []token, index int) token {
	if index >= 0 && index < len(tokens) {
		return tokens[index]
	}

	return token{kind: tokenOperator}
}

// memberName gives the bare name of a member symbol, as Add for
// Order.Add(Line,int) or Map for Result<>.Map<>(Func<T0,M0>).
func memberName(symbol *graph.Symbol) string {
	name := strings.TrimPrefix(symbol.Name, symbol.Parent+".")
	if cut := strings.IndexAny(name, "(<["); cut >= 0 {
		name = name[:cut]
	}

	return name
}

// simpleName gives the last part of a type's name, as Line<> for
// Order.Line<>.
func simpleName(name string) string {
	depth := 0
	for index := len(name) - 1; index >= 0; index-- {
		switch name[index] {
		case '>':
			depth++
		case '<':
			depth--
		case '.', '/':
			if depth == 0 {
				return name[index+1:]
			}
		}
	}

	return name
}

func parentNamespace(namespace string) string {
	if cut := strings.LastIndex(namespace, "."); cut >= 0 {
		return namespace[:cut]
	}

	return ""
}
