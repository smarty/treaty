package golang

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/smarty/treaty/internal/rules"
)

// coverPackage is the package instrumented code reports to. It exists only
// in the overlay a run builds with, beside the Go module's go.mod.
const coverPackage = "treatycover"

// coverRuntime is the source of coverPackage. %s is the size of every
// instrumented file's counters, in file order.
//
// A test calls S when it starts, and the function S returns when it ends,
// with its subtests. Between the two, every counter that rose is the
// test's. A test that ran while another did is shared, since the counters
// cannot tell them apart. Each test's record is a line of JSON in a file of
// the directory TREATY_COVER_DIR names, one file per test binary.
const coverRuntime = `package treatycover

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

var sizes = []int{%s}

var counts = func() [][]uint32 {
	result := make([][]uint32, len(sizes))
	for index, size := range sizes {
		result[index] = make([]uint32, size)
	}

	return result
}()

var (
	mutex  sync.Mutex
	active = map[*run]bool{}
	out    *os.File
)

type run struct {
	start  [][]uint32
	shared bool
}

type record struct {
	Module string   ` + "`json:\"m\"`" + `
	Test   string   ` + "`json:\"t\"`" + `
	Shared bool     ` + "`json:\"s\"`" + `
	Hits   [][2]int ` + "`json:\"h\"`" + `
}

func H(file, probe int) { atomic.AddUint32(&counts[file][probe], 1) }

func D(file, probe int, value bool) bool {
	if value {
		H(file, probe)
	} else {
		H(file, probe+1)
	}

	return value
}

func C[T cmp.Ordered](file, probe int, value, constant T) int {
	result := cmp.Compare(value, constant)
	H(file, probe+1+result)
	return result
}

func L[T cmp.Ordered](file, probe int, constant, value T) int {
	result := cmp.Compare(constant, value)
	H(file, probe+1-result)
	return result
}

func S(module, test string) func() {
	mutex.Lock()
	defer mutex.Unlock()
	current := &run{start: snapshot(), shared: len(active) > 0}
	for other := range active {
		other.shared = true
	}

	active[current] = true
	return func() { end(module, test, current) }
}

func end(module, test string, current *run) {
	mutex.Lock()
	defer mutex.Unlock()
	delete(active, current)
	each := record{Module: module, Test: test, Shared: current.shared}
	for file := range counts {
		for probe := range counts[file] {
			if atomic.LoadUint32(&counts[file][probe]) > current.start[file][probe] {
				each.Hits = append(each.Hits, [2]int{file, probe})
			}
		}
	}

	dir := os.Getenv("TREATY_COVER_DIR")
	if dir == "" {
		return
	}

	if out == nil {
		var err error
		if out, err = os.OpenFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return
		}
	}

	line, _ := json.Marshal(each)
	_, _ = out.Write(append(line, '\n'))
}

func snapshot() [][]uint32 {
	result := make([][]uint32, len(counts))
	for file := range counts {
		result[file] = make([]uint32, len(counts[file]))
		for probe := range counts[file] {
			result[file][probe] = atomic.LoadUint32(&counts[file][probe])
		}
	}

	return result
}
`

// comparisons are the operators a boundary probe can stand in for.
var comparisons = []string{"<", "<=", ">", ">=", "==", "!="}

// instrumenter rewrites one Go file so it counts what runs: every block,
// every outcome of an if or for condition and of each operand of && or ||
// in one, and where a value falls against an exported constant a condition
// compares it with. Every edit stays on its line, so lines keep their
// numbers.
type instrumenter struct {
	src       []byte
	tokens    []token
	file      int
	constants map[string]string
	probes    []rules.Probe
	edits     []edit
	lines     map[int]int
}

// edit inserts text at a byte offset, or replaces the bytes up to end when
// end is past at. Of the edits at one offset, closers come first, innermost
// first, then openers, outermost first.
type edit struct {
	at     int
	end    int
	text   string
	closer bool
	depth  int
}

// apply makes every edit to the source.
func (this *instrumenter) apply() []byte {
	slices.SortStableFunc(this.edits, func(a, b edit) int {
		switch {
		case a.at != b.at:
			return a.at - b.at
		case a.closer != b.closer:
			if a.closer {
				return -1
			}

			return 1
		case a.closer:
			return b.depth - a.depth
		default:
			return a.depth - b.depth
		}
	})

	var result []byte
	last := 0
	for _, each := range this.edits {
		if each.at < last {
			continue
		}

		result = append(append(result, this.src[last:each.at]...), each.text...)
		last = max(each.at, each.end)
	}

	return append(result, this.src[last:]...)
}

// block instruments the block whose opening brace is tokens[open] and
// returns the index of its closing brace.
func (this *instrumenter) block(open int) int {
	probe := this.probe(rules.Probe{Kind: rules.ProbeBlock, Line: this.tokens[open].line})
	this.edits = append(this.edits, edit{at: this.tokens[open].end, text: fmt.Sprintf("_tc.H(%d, %d);", this.file, probe)})
	return this.statements(open+1, probe, false)
}

// body finds the opening brace of the function body after tokens[from],
// skipping the braces of struct and interface types in its signature, or
// -1 when the function has none.
func (this *instrumenter) body(from int) int {
	depth := 0
	for index := from; index < len(this.tokens); index++ {
		current := this.tokens[index]
		switch {
		case current.is("(") || current.is("["):
			depth++
		case current.is(")") || current.is("]"):
			depth--
			if depth < 0 {
				return -1
			}
		case (current.is("struct") || current.is("interface")) && index+1 < len(this.tokens) && this.tokens[index+1].is("{"):
			index = this.matching(index + 1)
		case current.is("{") && depth == 0:
			return index
		case depth == 0 && (current.kind == tokenSemicolon || current.is(",") || current.is("}") || current.is("=")):
			return -1
		}
	}

	return -1
}

// boundary counts where a comparison's value falls against an exported
// constant, when tokens[start:end] compares exactly one with something that
// is not a constant.
func (this *instrumenter) boundary(start, end int) {
	op, depth := -1, 0
	for index := start; index < end; index++ {
		current := this.tokens[index]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			depth++
		case current.is(")") || current.is("]") || current.is("}"):
			depth--
		case depth == 0 && current.kind == tokenOperator && slices.Contains(comparisons, current.text):
			if op >= 0 {
				return
			}

			op = index
		}
	}

	if op <= start || op >= end-1 {
		return
	}

	left, right := this.constant(start, op), this.constant(op+1, end)
	if (left == "") == (right == "") || !this.variable(start, op) && left == "" || !this.variable(op+1, end) && right == "" {
		return
	}

	constant, call := right, "C"
	if left != "" {
		constant, call = left, "L"
	}

	text := this.tokens[op].text
	equality := text == "==" || text == "!="
	line := this.tokens[op].line
	column, endColumn := this.columns(start, end)
	if this.tokens[start].line != line {
		column, endColumn = 0, 0
	}

	first := this.probe(rules.Probe{Kind: rules.ProbeBelow, Line: line, Constant: constant, Equality: equality, Column: column, EndColumn: endColumn})
	this.probe(rules.Probe{Kind: rules.ProbeAt, Line: line, Group: first, Constant: constant, Equality: equality, Column: column, EndColumn: endColumn})
	this.probe(rules.Probe{Kind: rules.ProbeAbove, Line: line, Group: first, Constant: constant, Equality: equality, Column: column, EndColumn: endColumn})
	this.probes[first].Group = first
	this.edits = append(this.edits,
		edit{at: this.tokens[start].start, text: fmt.Sprintf("_tc.%s(%d, %d, ", call, this.file, first), depth: 2},
		edit{at: this.tokens[op].start, end: this.tokens[op].end, text: ","},
		edit{at: this.tokens[end-1].end, text: ") " + text + " 0", closer: true, depth: 2})
}

// columns spans tokens[start:end] on the line it starts on, in characters
// from 1: where it starts, and where it ends, or 0 when it ends on a later
// line.
func (this *instrumenter) columns(start, end int) (column, endColumn int) {
	from := this.tokens[start].start
	lineStart := from
	for lineStart > 0 && this.src[lineStart-1] != '\n' {
		lineStart--
	}

	column = utf8.RuneCount(this.src[lineStart:from]) + 1
	if last := this.tokens[end-1]; last.line == this.tokens[start].line {
		endColumn = column + utf8.RuneCount(this.src[from:last.end])
	}

	return column, endColumn
}

// composite reports whether the brace at tokens[open], in the header of an
// if, for or switch statement, opens a composite literal rather than the
// statement's block: a literal there must be parenthesized unless its type
// starts with [ or map, as in []int{1, 2}. A struct type's literal there is
// read as the block.
func (this *instrumenter) composite(open int) bool {
	index, names := open-1, 0
	for index >= 0 && (this.tokens[index].kind == tokenIdent && !isKeyword(this.tokens[index].text) || this.tokens[index].is(".") || this.tokens[index].is("*")) {
		if this.tokens[index].kind == tokenIdent {
			names++
		}

		index--
	}

	if index < 0 || names == 0 || !this.tokens[index].is("]") {
		return false
	}

	// The type's [ follows an operator, range or map, as in []T or
	// map[K]V; an index expression's follows what it indexes, as in x[i].
	for depth := 0; index >= 0; index-- {
		switch {
		case this.tokens[index].is("]"):
			depth++
		case this.tokens[index].is("["):
			depth--
		}

		if depth == 0 {
			break
		}
	}

	if index <= 0 {
		return index == 0
	}

	before := this.tokens[index-1]
	return before.is("map") || !(before.kind == tokenIdent && !isKeyword(before.text) || before.is(")") || before.is("]") || before.is("}") || before.kind == tokenString)
}

// condition counts a condition's outcomes, its operands' outcomes when it
// joins several with && or ||, and the comparisons with exported constants
// among them.
func (this *instrumenter) condition(start, end int) {
	if start >= end {
		return
	}

	line := this.tokens[start].line
	column, endColumn := this.columns(start, end)
	group := this.probe(rules.Probe{Kind: rules.ProbeTrue, Line: line, Column: column, EndColumn: endColumn})
	this.probe(rules.Probe{Kind: rules.ProbeFalse, Line: line, Group: group, Column: column, EndColumn: endColumn})
	this.probes[group].Group = group
	this.edits = append(this.edits,
		edit{at: this.tokens[start].start, text: fmt.Sprintf("_tc.D(%d, %d, ", this.file, group)},
		edit{at: this.tokens[end-1].end, text: ")", closer: true})

	operands := this.operands(start, end)
	for _, operand := range operands {
		if len(operands) > 1 {
			line := this.tokens[operand[0]].line
			column, endColumn := this.columns(operand[0], operand[1])
			first := this.probe(rules.Probe{Kind: rules.ProbeTrue, Line: line, Operand: true, Column: column, EndColumn: endColumn})
			this.probe(rules.Probe{Kind: rules.ProbeFalse, Line: line, Operand: true, Group: first, Column: column, EndColumn: endColumn})
			this.probes[first].Group = first
			this.edits = append(this.edits,
				edit{at: this.tokens[operand[0]].start, text: fmt.Sprintf("_tc.D(%d, %d, (", this.file, first), depth: 1},
				edit{at: this.tokens[operand[1]-1].end, text: "))", closer: true, depth: 1})
		}

		this.boundary(operand[0], operand[1])
	}
}

// constant names the exported constant tokens[start:end] is, as Name or
// pkg.Name, by symbol id, or is empty.
func (this *instrumenter) constant(start, end int) string {
	switch end - start {
	case 1:
		return this.constants[this.tokens[start].text]
	case 3:
		if this.tokens[start+1].is(".") {
			return this.constants[this.tokens[start].text+"."+this.tokens[start+2].text]
		}
	}

	return ""
}

// forStatement instruments a for statement at tokens[at], counting its
// condition when it has one, and returns the index after it.
func (this *instrumenter) forStatement(at int) int {
	open := this.header(at + 1)
	if open < 0 {
		return len(this.tokens)
	}

	var semicolons []int
	ranged, depth := false, 0
	for index := at + 1; index < open; index++ {
		current := this.tokens[index]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			depth++
		case current.is(")") || current.is("]") || current.is("}"):
			depth--
		case depth == 0 && current.is("range"):
			ranged = true
		case depth == 0 && current.kind == tokenSemicolon:
			semicolons = append(semicolons, index)
		}
	}

	switch {
	case ranged:
	case len(semicolons) == 2:
		this.condition(semicolons[0]+1, semicolons[1])
	case len(semicolons) == 0:
		this.condition(at+1, open)
	}

	return this.block(open) + 1
}

// function instruments the function at tokens[at], a declaration or a
// literal, and returns the index of its body's closing brace, or at when
// it has no body.
func (this *instrumenter) function(at int) int {
	// After ], chan or *, func is a type, as in map[K]func() or []func(),
	// and a brace after it opens a composite literal, not a body.
	if at > 0 && (this.tokens[at-1].is("]") || this.tokens[at-1].is("chan") || this.tokens[at-1].is("*")) {
		return at
	}

	open := this.body(at + 1)
	if open < 0 {
		return at
	}

	return this.block(open)
}

// header finds the opening brace of the block of the if, for or switch
// statement whose header starts at tokens[from], instrumenting function
// literals in it, or -1.
func (this *instrumenter) header(from int) int {
	depth := 0
	for index := from; index < len(this.tokens); index++ {
		current := this.tokens[index]
		switch {
		case current.is("func"):
			index = this.function(index)
		case current.is("(") || current.is("["):
			depth++
		case current.is(")") || current.is("]"):
			depth--
		case current.is("{") && depth == 0 && this.composite(index):
			index = this.matching(index)
		case current.is("{") && depth == 0:
			return index
		}
	}

	return -1
}

// ifStatement instruments an if statement at tokens[at], with its else
// branches, and returns the index after it.
func (this *instrumenter) ifStatement(at int) int {
	open := this.header(at + 1)
	if open < 0 {
		return len(this.tokens)
	}

	start, depth := at+1, 0
	for index := at + 1; index < open; index++ {
		current := this.tokens[index]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			depth++
		case current.is(")") || current.is("]") || current.is("}"):
			depth--
		case depth == 0 && current.kind == tokenSemicolon:
			start = index + 1
		}
	}

	this.condition(start, open)
	next := this.block(open) + 1
	if next < len(this.tokens) && this.tokens[next].is("else") {
		switch {
		case next+1 < len(this.tokens) && this.tokens[next+1].is("if"):
			return this.ifStatement(next + 1)
		case next+1 < len(this.tokens) && this.tokens[next+1].is("{"):
			return this.block(next+1) + 1
		}
	}

	return next
}

// matching finds the brace that closes the one at tokens[open].
func (this *instrumenter) matching(open int) int {
	depth := 0
	for index := open; index < len(this.tokens); index++ {
		switch {
		case this.tokens[index].is("{"):
			depth++
		case this.tokens[index].is("}"):
			depth--
			if depth == 0 {
				return index
			}
		}
	}

	return len(this.tokens) - 1
}

// operands splits tokens[start:end] at the && and || outside brackets.
func (this *instrumenter) operands(start, end int) (result [][2]int) {
	from, depth := start, 0
	for index := start; index < end; index++ {
		current := this.tokens[index]
		switch {
		case current.is("(") || current.is("[") || current.is("{"):
			depth++
		case current.is(")") || current.is("]") || current.is("}"):
			depth--
		case depth == 0 && (current.is("&&") || current.is("||")):
			result = append(result, [2]int{from, index})
			from = index + 1
		}
	}

	return append(result, [2]int{from, end})
}

// probe adds a probe and returns its index.
func (this *instrumenter) probe(probe rules.Probe) int {
	this.probes = append(this.probes, probe)
	return len(this.probes) - 1
}

// simple skips a statement that holds no block of its own, instrumenting
// function literals in it, and returns the index of the token that ends it.
func (this *instrumenter) simple(at int) int {
	depth := 0
	for index := at; index < len(this.tokens); index++ {
		current := this.tokens[index]
		switch {
		case current.is("func"):
			index = this.function(index)
		case current.is("(") || current.is("[") || current.is("{"):
			depth++
		case current.is(")") || current.is("]"):
			depth--
		case current.is("}"):
			if depth == 0 {
				return index
			}

			depth--
		case current.kind == tokenSemicolon && depth == 0:
			return index
		}
	}

	return len(this.tokens)
}

// statements instruments a statement list from tokens[at] up to the brace
// that closes its block or, in a clause, the next case or default, and
// returns where it stopped. Each statement's first line is the block's.
// As Go's cover tool does, the statements after an if, for, switch, select
// or nested block start a block of their own, since the one before may not
// let control reach them, as when an if returns.
func (this *instrumenter) statements(at, block int, clause bool) int {
	index, branched := at, false
	for index < len(this.tokens) {
		current := this.tokens[index]
		switch {
		case current.is("}"):
			return index
		case clause && (current.is("case") || current.is("default")):
			return index
		case current.kind == tokenSemicolon:
			index++
			continue
		}

		if branched {
			branched = false
			block = this.probe(rules.Probe{Kind: rules.ProbeBlock, Line: current.line})
			this.edits = append(this.edits, edit{at: current.start, text: fmt.Sprintf("_tc.H(%d, %d); ", this.file, block)})
			this.lines[current.line] = block
		}

		if _, ok := this.lines[current.line]; !ok {
			this.lines[current.line] = block
		}

		switch {
		case current.kind == tokenIdent && !isKeyword(current.text) && index+1 < len(this.tokens) && this.tokens[index+1].is(":"):
			index += 2
			continue
		case current.is("if"):
			index = this.ifStatement(index)
		case current.is("for"):
			index = this.forStatement(index)
		case current.is("switch") || current.is("select"):
			index = this.switchStatement(index)
		case current.is("{"):
			index = this.block(index) + 1
		default:
			index = this.simple(index)
			continue
		}

		branched = true
	}

	return index
}

// switchStatement instruments a switch or select statement at tokens[at],
// counting each clause as a block, and returns the index after it.
func (this *instrumenter) switchStatement(at int) int {
	open := this.header(at + 1)
	if open < 0 {
		return len(this.tokens)
	}

	index := open + 1
	for index < len(this.tokens) && !this.tokens[index].is("}") {
		current := this.tokens[index]
		if !current.is("case") && !current.is("default") {
			index++
			continue
		}

		colon, depth := index+1, 0
		for ; colon < len(this.tokens); colon++ {
			token := this.tokens[colon]
			switch {
			case token.is("func"):
				colon = this.function(colon)
			case token.is("(") || token.is("[") || token.is("{"):
				depth++
			case token.is(")") || token.is("]") || token.is("}"):
				depth--
			}

			if depth == 0 && token.is(":") {
				break
			}
		}

		if colon >= len(this.tokens) {
			return colon
		}

		probe := this.probe(rules.Probe{Kind: rules.ProbeBlock, Line: current.line})
		this.edits = append(this.edits, edit{at: this.tokens[colon].end, text: fmt.Sprintf("_tc.H(%d, %d);", this.file, probe)})
		index = this.statements(colon+1, probe, true)
	}

	return index + 1
}

// testing reports whether a function named name, whose parameters start at
// tokens[at], is one go test runs with a *testing.T or *testing.F, as
// TestXxx and FuzzXxx are; TestMain and helpers are not.
func (this *instrumenter) testing(at int, name string) bool {
	if at+5 >= len(this.tokens) || this.tokens[at].kind != tokenIdent || !this.tokens[at+1].is("*") || !this.tokens[at+2].is("testing") || !this.tokens[at+3].is(".") {
		return false
	}

	for prefix, kind := range map[string]string{"Test": "T", "Fuzz": "F"} {
		rest, ok := strings.CutPrefix(name, prefix)
		if ok && this.tokens[at+4].is(kind) && this.tokens[at+5].is(")") && (rest == "" || !strings.ContainsAny(rest[:1], "abcdefghijklmnopqrstuvwxyz")) {
			return true
		}
	}

	return false
}

// variable reports whether tokens[start:end] could hold a value that varies:
// it names something, rather than being only literals.
func (this *instrumenter) variable(start, end int) bool {
	for index := start; index < end; index++ {
		if this.tokens[index].kind == tokenIdent && !isKeyword(this.tokens[index].text) {
			return true
		}
	}

	return false
}

// instrument rewrites a file's source to count what runs.
//
// Parameters:
//   - src: the file's source.
//   - file: the file's index among the run's instrumented files.
//   - importPath: the import path of coverPackage.
//   - constants: how the file may name an exported constant, as Name or
//     pkg.Name, mapped to its symbol id.
//
// Returns:
//   - result: the rewritten source.
//   - probes: what it counts, by counter index.
func instrument(src []byte, file int, importPath string, constants map[string]string) (result []byte, probes []rules.Probe) {
	this := &instrumenter{src: src, tokens: lex(src), file: file, constants: constants, lines: map[int]int{}}
	if len(this.tokens) < 2 || !this.tokens[0].is("package") {
		return src, nil
	}

	for index := 0; index < len(this.tokens); index++ {
		if this.tokens[index].is("func") {
			index = this.function(index)
		}
	}

	for line, block := range this.lines {
		this.probes[block].Lines = append(this.probes[block].Lines, line)
	}

	for index := range this.probes {
		slices.Sort(this.probes[index].Lines)
	}

	header := fmt.Sprintf("; import _tc %q", importPath)
	this.edits = append(this.edits, edit{at: this.tokens[1].end, text: header}, edit{at: len(src), text: "\nvar _ = _tc.H\n"})
	return this.apply(), this.probes
}

// instrumentTests rewrites a test file so each Test, Fuzz and Example
// function reports the counters that rose while it ran, with its subtests.
//
// Parameters:
//   - src: the test file's source.
//   - module: the module id its tests belong to.
//   - importPath: the import path of coverPackage.
//
// Returns:
//   - result: the rewritten source, or src when it declares no tests.
//   - changed: it declares tests, so result differs.
func instrumentTests(src []byte, module, importPath string) (result []byte, changed bool) {
	this := &instrumenter{src: src, tokens: lex(src)}
	if len(this.tokens) < 2 || !this.tokens[0].is("package") {
		return src, false
	}

	depth := 0
	for index := 0; index+2 < len(this.tokens); index++ {
		current := this.tokens[index]
		switch {
		case current.is("{"):
			depth++
		case current.is("}"):
			depth--
		case depth == 0 && current.is("func") && this.tokens[index+1].kind == tokenIdent && this.tokens[index+2].is("("):
			name := this.tokens[index+1].text
			open := this.body(index + 1)
			if open < 0 {
				continue
			}

			call := fmt.Sprintf("_tc.S(%q, %q)", module, name)
			parameter := this.tokens[index+3]
			switch {
			case strings.HasPrefix(name, "Example") && this.tokens[index+3].is(")"):
				this.edits = append(this.edits, edit{at: this.tokens[open].end, text: "defer " + call + "();"})
			case this.testing(index+3, name) && parameter.text != "_":
				this.edits = append(this.edits, edit{at: this.tokens[open].end, text: parameter.text + ".Cleanup(" + call + ");"})
			default:
				continue
			}

			changed = true
		}
	}

	if !changed {
		return src, false
	}

	this.edits = append(this.edits, edit{at: this.tokens[1].end, text: fmt.Sprintf("; import _tc %q", importPath)})
	return this.apply(), true
}
