// Package javascript reads JavaScript and TypeScript source at the level of
// contracts: imports and exports, top-level declarations and their
// signatures, class and interface members, plus the identifiers inside
// bodies that become reference edges. It is hand-written and does not parse
// expressions or statements; bodies are only scanned as tokens.
package javascript

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	tokenIdent tokenKind = iota
	tokenNumber
	tokenString
	tokenOperator
)

// operators are the multi-character operators, longest first. Angle
// brackets are always single tokens, so nested type arguments such as
// Map<string, Array<number>> close one bracket at a time.
var operators = []string{
	"...", "===", "!==", "**=", "&&=", "||=", "??=",
	"=>", "==", "!=", "&&", "||", "??", "?.", "++", "--", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "**",
}

// token is one lexical token with its position in the source.
type token struct {
	kind    tokenKind
	text    string
	line    int
	start   int
	end     int
	newline bool
}

// tokenKind classifies a token.
type tokenKind int

// lexer splits one file into tokens. JSX elements and template literals
// call back into code for their embedded expressions.
type lexer struct {
	src     []byte
	pos     int
	line    int
	jsx     bool
	newline bool
	tokens  []token
}

// is reports whether the token is the given operator or identifier text.
func (this token) is(text string) bool {
	return (this.kind == tokenOperator || this.kind == tokenIdent) && this.text == text
}

// lex splits JavaScript or TypeScript source into tokens, dropping comments.
// Each token records whether it starts a line, which stands in for
// automatic semicolon insertion. Strings, template text and regular
// expressions become string tokens; JSX contributes only its tag names and
// embedded expressions.
//
// Parameters:
//   - src: the source.
//   - jsx: whether the file may hold JSX, which plain TypeScript files
//     cannot, since there <T> is a type assertion.
//
// Returns:
//   - result: the tokens.
func lex(src []byte, jsx bool) []token {
	this := &lexer{src: src, line: 1, jsx: jsx, newline: true}
	if strings.HasPrefix(string(src), "#!") {
		this.skipLine()
	}

	this.code(false)
	return this.tokens
}

// code lexes until the end of the source or, when nested, until the brace
// that closes an embedded expression, which it consumes.
func (this *lexer) code(nested bool) {
	depth := 0
	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\n':
			this.line++
			this.newline = true
			this.pos++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			this.pos++
		case c == '/' && this.peek(1) == '/':
			this.skipLine()
		case c == '/' && this.peek(1) == '*':
			end := strings.Index(string(this.src[this.pos+2:]), "*/")
			if end < 0 {
				end = len(this.src) - this.pos - 2
			}

			if newlines := strings.Count(string(this.src[this.pos:this.pos+2+end]), "\n"); newlines > 0 {
				this.line += newlines
				this.newline = true
			}

			this.pos = min(this.pos+2+end+2, len(this.src))
		case c == '"' || c == '\'':
			this.quoted(c)
		case c == '`':
			this.template()
		case c == '/' && this.expressionNext():
			this.regex()
		case c == '<' && this.jsx && this.expressionNext() && this.jsxNext():
			this.element()
		case isDigit(c) || (c == '.' && isDigit(this.peek(1))):
			start := this.pos
			for this.pos < len(this.src) {
				current := this.src[this.pos]
				if !(isDigit(current) || isLetter(current) || current == '.' || current == '_' ||
					((current == '+' || current == '-') && strings.ContainsRune("eE", rune(this.src[this.pos-1])) && !strings.HasPrefix(string(this.src[start:]), "0x"))) {
					break
				}

				this.pos++
			}

			this.emit(tokenNumber, start)
		case c == '#' && identStart(this.src, this.pos+1):
			start := this.pos
			this.pos++
			this.identifier()
			this.emit(tokenIdent, start)
		case identStart(this.src, this.pos):
			start := this.pos
			this.identifier()
			this.emit(tokenIdent, start)
		case c == '}' && nested && depth == 0:
			this.pos++
			return
		default:
			_, size := utf8.DecodeRune(this.src[this.pos:])
			text := string(this.src[this.pos : this.pos+size])
			for _, operator := range operators {
				if strings.HasPrefix(string(this.src[this.pos:min(this.pos+len(operator), len(this.src))]), operator) {
					text = operator
					break
				}
			}

			switch text {
			case "{":
				depth++
			case "}":
				depth--
			}

			start := this.pos
			this.pos += len(text)
			this.emit(tokenOperator, start)
		}
	}
}

// element skips one JSX element, keeping its tag name and the expressions
// in its attributes and children.
func (this *lexer) element() {
	this.pos++
	this.space()
	if this.peek(0) == '>' {
		this.pos++
		this.children()
		return
	}

	for identStart(this.src, this.pos) {
		start := this.pos
		this.identifier()
		for this.pos < len(this.src) && (this.src[this.pos] == '-' || this.src[this.pos] == ':') {
			this.pos++
			this.identifier()
		}

		this.emit(tokenIdent, start)
		if this.peek(0) != '.' {
			break
		}

		this.pos++
		this.emit(tokenOperator, this.pos-1)
	}

	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\n':
			this.line++
			this.newline = true
			this.pos++
		case c == '/' && this.peek(1) == '>':
			this.pos += 2
			return
		case c == '>':
			this.pos++
			this.children()
			return
		case c == '{':
			this.pos++
			this.code(true)
		case c == '"' || c == '\'':
			this.pos++
			for this.pos < len(this.src) && this.src[this.pos] != c {
				if this.src[this.pos] == '\n' {
					this.line++
				}

				this.pos++
			}

			this.pos++
		default:
			this.pos++
		}
	}
}

// children skips JSX text up to the element's closing tag, lexing nested
// elements and embedded expressions.
func (this *lexer) children() {
	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\n':
			this.line++
			this.newline = true
			this.pos++
		case c == '<' && this.peek(1) == '/':
			for this.pos < len(this.src) && this.src[this.pos] != '>' {
				this.pos++
			}

			this.pos++
			return
		case c == '<':
			this.element()
		case c == '{':
			this.pos++
			this.code(true)
		default:
			this.pos++
		}
	}
}

func (this *lexer) emit(kind tokenKind, start int) {
	this.tokens = append(this.tokens, token{kind: kind, text: string(this.src[start:this.pos]), line: this.line, start: start, end: this.pos, newline: this.newline})
	this.newline = false
}

// expressionNext reports whether an expression may start here, which tells
// a regular expression from division and JSX from less-than.
func (this *lexer) expressionNext() bool {
	if len(this.tokens) == 0 {
		return true
	}

	last := this.tokens[len(this.tokens)-1]
	switch last.kind {
	case tokenNumber, tokenString:
		return false
	case tokenIdent:
		switch last.text {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await", "default", "extends":
			return true
		}

		return false
	default:
		return last.text != ")" && last.text != "]" && last.text != "}"
	}
}

func (this *lexer) identifier() {
	for this.pos < len(this.src) {
		r, size := utf8.DecodeRune(this.src[this.pos:])
		if !(r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			break
		}

		this.pos += size
	}
}

// jsxNext tells a JSX element from a TSX arrow function's type parameters,
// which read <T,> or <T extends U>.
func (this *lexer) jsxNext() bool {
	next := this.peek(1)
	if next == '>' {
		return true
	}

	if !identStart(this.src, this.pos+1) {
		return false
	}

	rest := string(this.src[this.pos+1:])
	end := strings.IndexFunc(rest, func(r rune) bool {
		return !(r == '_' || r == '$' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	if end < 0 {
		return true
	}

	after := strings.TrimLeft(rest[end:], " \t")
	return !strings.HasPrefix(after, ",") && !strings.HasPrefix(after, "extends ")
}

func (this *lexer) peek(offset int) byte {
	if this.pos+offset < len(this.src) {
		return this.src[this.pos+offset]
	}

	return 0
}

func (this *lexer) quoted(quote byte) {
	start := this.pos
	this.pos++
	for this.pos < len(this.src) && this.src[this.pos] != quote && this.src[this.pos] != '\n' {
		if this.src[this.pos] == '\\' {
			if this.peek(1) == '\n' {
				this.line++
			}

			this.pos++
		}

		this.pos++
	}

	this.pos = min(this.pos+1, len(this.src))
	this.emit(tokenString, start)
}

// regex skips a regular expression literal, whose slashes may sit inside a
// character class.
func (this *lexer) regex() {
	start := this.pos
	this.pos++
	class := false
	for this.pos < len(this.src) && this.src[this.pos] != '\n' {
		c := this.src[this.pos]
		if c == '\\' {
			this.pos += 2
			continue
		}

		if c == '[' {
			class = true
		} else if c == ']' {
			class = false
		} else if c == '/' && !class {
			break
		}

		this.pos++
	}

	this.pos = min(this.pos+1, len(this.src))
	for this.pos < len(this.src) && isLetter(this.src[this.pos]) {
		this.pos++
	}

	this.emit(tokenString, start)
}

func (this *lexer) skipLine() {
	for this.pos < len(this.src) && this.src[this.pos] != '\n' {
		this.pos++
	}
}

func (this *lexer) space() {
	for this.pos < len(this.src) && strings.IndexByte(" \t\r\n", this.src[this.pos]) >= 0 {
		if this.src[this.pos] == '\n' {
			this.line++
			this.newline = true
		}

		this.pos++
	}
}

// template lexes a template literal: its text becomes string tokens and
// each ${...} is lexed as code.
func (this *lexer) template() {
	start := this.pos
	startLine := this.line
	this.pos++
	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\\':
			if this.peek(1) == '\n' {
				this.line++
			}

			this.pos += 2
		case c == '`':
			this.pos++
			this.tokens = append(this.tokens, token{kind: tokenString, text: string(this.src[start:this.pos]), line: startLine, start: start, end: this.pos, newline: this.newline})
			this.newline = false
			return
		case c == '$' && this.peek(1) == '{':
			this.pos += 2
			this.tokens = append(this.tokens, token{kind: tokenString, text: string(this.src[start:this.pos]), line: startLine, start: start, end: this.pos, newline: this.newline})
			this.newline = false
			this.code(true)
			start, startLine = this.pos-1, this.line
		case c == '\n':
			this.line++
			this.pos++
		default:
			this.pos++
		}
	}

	this.pos = len(this.src)
}

func identStart(src []byte, at int) bool {
	if at >= len(src) {
		return false
	}

	r, _ := utf8.DecodeRune(src[at:])
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isKeyword reports whether an identifier is a reserved word or a
// TypeScript keyword that never names a declaration a reference could reach.
func isKeyword(text string) bool {
	switch text {
	case "abstract", "as", "async", "await", "break", "case", "catch", "class", "const", "continue", "debugger", "declare",
		"default", "delete", "do", "else", "enum", "export", "extends", "false", "finally", "for", "from", "function", "get",
		"if", "implements", "import", "in", "instanceof", "interface", "keyof", "let", "new", "null", "of", "private",
		"protected", "public", "readonly", "return", "satisfies", "set", "static", "super", "switch", "this", "throw",
		"true", "try", "type", "typeof", "undefined", "var", "void", "while", "with", "yield":
		return true
	}

	return false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
