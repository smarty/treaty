// Package python reads Python source at the level of contracts: imports,
// top-level functions, classes, their methods and attributes, and module
// variables, plus the identifiers inside bodies that become reference
// edges. It is hand-written and does not parse expressions or statements;
// bodies are only scanned as tokens.
package python

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

// operators are the multi-character operators, longest first.
var operators = []string{
	"**=", "//=", ">>=", "<<=", "...",
	"->", ":=", "**", "//", "==", "!=", "<=", ">=", "<<", ">>", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "@=",
}

// token is one lexical token with its position in the source. The first
// token of each logical line records that line's indentation.
type token struct {
	kind    tokenKind
	text    string
	line    int
	start   int
	end     int
	newline bool
	indent  int
}

// tokenKind classifies a token.
type tokenKind int

// lexer splits one file into tokens. An f-string's replacement fields are
// lexed as code.
type lexer struct {
	src       []byte
	pos       int
	line      int
	depth     int
	lineStart bool
	column    int
	tokens    []token
}

// is reports whether the token is the given operator or identifier text.
func (this token) is(text string) bool {
	return (this.kind == tokenOperator || this.kind == tokenIdent) && this.text == text
}

// lex splits Python source into tokens, dropping comments. A line break
// inside brackets or after a backslash continues the logical line.
//
// Parameters:
//   - src: the source.
//
// Returns:
//   - result: the tokens.
func lex(src []byte) []token {
	this := &lexer{src: src, line: 1, lineStart: true}
	this.code(false)
	return this.tokens
}

// code lexes until the end of the source or, when nested in an f-string,
// until the brace that closes the replacement field, which it consumes.
func (this *lexer) code(nested bool) {
	depth := 0
	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\n':
			this.line++
			this.pos++
			if this.depth == 0 && !nested {
				this.lineStart, this.column = true, 0
			}
		case c == '\\' && this.peek(1) == '\n':
			this.line++
			this.pos += 2
		case c == '\\' && this.peek(1) == '\r' && this.peek(2) == '\n':
			this.line++
			this.pos += 3
		case c == ' ' || c == '\f' || c == '\r':
			this.column++
			this.pos++
		case c == '\t':
			this.column = (this.column/8 + 1) * 8
			this.pos++
		case c == '#':
			for this.pos < len(this.src) && this.src[this.pos] != '\n' {
				this.pos++
			}
		case c == '}' && nested && depth == 0:
			this.pos++
			return
		case this.stringNext():
			this.quoted()
		case isDigit(c) || (c == '.' && isDigit(this.peek(1))):
			start := this.pos
			for this.pos < len(this.src) {
				current := this.src[this.pos]
				if !(isDigit(current) || isLetter(current) || current == '.' || current == '_' ||
					((current == '+' || current == '-') && (this.src[this.pos-1] == 'e' || this.src[this.pos-1] == 'E') && !strings.HasPrefix(strings.ToLower(string(this.src[start:])), "0x"))) {
					break
				}

				this.pos++
			}

			this.emit(tokenNumber, start)
		case identStart(this.src, this.pos):
			start := this.pos
			for this.pos < len(this.src) {
				r, size := utf8.DecodeRune(this.src[this.pos:])
				if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
					break
				}

				this.pos += size
			}

			this.emit(tokenIdent, start)
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
			case "(", "[", "{":
				this.depth++
				depth++
			case ")", "]", "}":
				this.depth = max(this.depth-1, 0)
				depth--
			}

			start := this.pos
			this.pos += len(text)
			this.emit(tokenOperator, start)
		}
	}
}

func (this *lexer) emit(kind tokenKind, start int) {
	this.tokens = append(this.tokens, token{kind: kind, text: string(this.src[start:this.pos]), line: this.line, start: start, end: this.pos, newline: this.lineStart, indent: this.column})
	this.lineStart = false
}

func (this *lexer) peek(offset int) byte {
	if this.pos+offset < len(this.src) {
		return this.src[this.pos+offset]
	}

	return 0
}

// quoted lexes a string with its prefix: raw, bytes, formatted, and
// triple-quoted forms. An f-string's text becomes string tokens and each
// replacement field is lexed as code.
func (this *lexer) quoted() {
	start, startLine := this.pos, this.line
	for this.src[this.pos] != '"' && this.src[this.pos] != '\'' {
		this.pos++
	}

	prefix := strings.ToLower(string(this.src[start:this.pos]))
	formatted := strings.Contains(prefix, "f") || strings.Contains(prefix, "t")
	quote := string(this.src[this.pos])
	if strings.HasPrefix(string(this.src[this.pos:]), strings.Repeat(quote, 3)) {
		quote = strings.Repeat(quote, 3)
	}

	this.pos += len(quote)
	piece := func() {
		this.tokens = append(this.tokens, token{kind: tokenString, text: string(this.src[start:this.pos]), line: startLine, start: start, end: this.pos, newline: this.lineStart, indent: this.column})
		this.lineStart = false
	}

	for this.pos < len(this.src) {
		c := this.src[this.pos]
		switch {
		case c == '\\':
			if this.peek(1) == '\n' {
				this.line++
			}

			this.pos += 2
		case strings.HasPrefix(string(this.src[this.pos:]), quote):
			this.pos += len(quote)
			piece()
			return
		case c == '\n' && len(quote) == 1:
			piece()
			return
		case c == '\n':
			this.line++
			this.pos++
		case formatted && (c == '{' || c == '}') && this.peek(1) == c:
			this.pos += 2
		case formatted && c == '{':
			this.pos++
			piece()
			depth := this.depth
			this.depth++
			this.code(true)
			this.depth = depth
			start, startLine = this.pos-1, this.line
		default:
			this.pos++
		}
	}

	this.pos = min(this.pos, len(this.src))
	piece()
}

// stringNext reports whether a string, with a prefix of up to two letters
// such as rb or f, starts here. Identifiers are lexed whole, so a prefix
// is never the tail of one.
func (this *lexer) stringNext() bool {
	for index := this.pos; index < len(this.src) && index-this.pos <= 2; index++ {
		c := this.src[index]
		if c == '"' || c == '\'' {
			return true
		}

		if !strings.ContainsRune("rRbBuUfFtT", rune(c)) {
			return false
		}
	}

	return false
}

func identStart(src []byte, at int) bool {
	if at < 0 || at >= len(src) {
		return false
	}

	r, _ := utf8.DecodeRune(src[at:])
	return r == '_' || unicode.IsLetter(r)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isKeyword reports whether an identifier is a reserved word, or a name
// such as self that never names a declaration a reference could reach.
func isKeyword(text string) bool {
	switch text {
	case "False", "None", "True", "and", "as", "assert", "async", "await", "break", "class", "continue", "def", "del",
		"elif", "else", "except", "finally", "for", "from", "global", "if", "import", "in", "is", "lambda", "nonlocal",
		"not", "or", "pass", "raise", "return", "try", "while", "with", "yield", "self", "cls":
		return true
	}

	return false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
