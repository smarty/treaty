// Package golang reads Go source at the level of contracts: package clauses,
// imports, top-level declarations and their signatures, plus the identifiers
// inside bodies that become reference edges. It is hand-written and does
// not parse expressions or statements; bodies are only scanned as tokens.
package golang

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	tokenIdent tokenKind = iota
	tokenNumber
	tokenString
	tokenChar
	tokenOperator
	tokenSemicolon
)

var operators = []string{
	"<<=", ">>=", "&^=", "...",
	"&&", "||", "<-", "++", "--", "==", "!=", "<=", ">=", ":=",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>", "&^",
}

// token is one lexical token with its position in the source.
type token struct {
	kind  tokenKind
	text  string
	line  int
	start int
	end   int
	auto  bool
}

// tokenKind classifies a token.
type tokenKind int

// is reports whether the token is the given operator or identifier text.
func (this token) is(text string) bool {
	return (this.kind == tokenOperator || this.kind == tokenIdent || this.kind == tokenSemicolon) && this.text == text
}

// lex splits Go source into tokens, dropping comments and inserting
// semicolons at line ends the way the Go specification does.
func lex(src []byte) []token {
	var result []token
	line := 1
	i := 0
	insertSemicolon := func(at int) {
		if len(result) == 0 {
			return
		}

		last := result[len(result)-1]
		switch {
		case last.kind == tokenIdent && (last.text == "break" || last.text == "continue" || last.text == "fallthrough" || last.text == "return" || !isKeyword(last.text)),
			last.kind == tokenNumber, last.kind == tokenString, last.kind == tokenChar,
			last.kind == tokenOperator && (last.text == "++" || last.text == "--" || last.text == ")" || last.text == "]" || last.text == "}"):
			result = append(result, token{kind: tokenSemicolon, text: ";", line: line, start: at, end: at, auto: true})
		}
	}

	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			insertSemicolon(i)
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(string(src[i+2:]), "*/")
			if end < 0 {
				end = len(src) - i - 2
			}

			comment := src[i : i+2+end]
			if newlines := strings.Count(string(comment), "\n"); newlines > 0 {
				insertSemicolon(i)
				line += newlines
			}

			i += 2 + end + 2
		case c == '"' || c == '\'':
			start := i
			i++
			for i < len(src) && src[i] != c && src[i] != '\n' {
				if src[i] == '\\' {
					i++
				}

				i++
			}

			i = min(i+1, len(src))
			kind := tokenString
			if c == '\'' {
				kind = tokenChar
			}

			result = append(result, token{kind: kind, text: string(src[start:i]), line: line, start: start, end: i})
		case c == '`':
			start, startLine := i, line
			i++
			for i < len(src) && src[i] != '`' {
				if src[i] == '\n' {
					line++
				}

				i++
			}

			i = min(i+1, len(src))
			result = append(result, token{kind: tokenString, text: string(src[start:i]), line: startLine, start: start, end: i})
		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1])):
			start := i
			for i < len(src) && (isDigit(src[i]) || isLetter(src[i]) || src[i] == '.' || src[i] == '_' ||
				((src[i] == '+' || src[i] == '-') && strings.ContainsRune("eEpP", rune(src[i-1])))) {
				i++
			}

			result = append(result, token{kind: tokenNumber, text: string(src[start:i]), line: line, start: start, end: i})
		case isLetter(c) || c == '_' || c >= utf8.RuneSelf:
			start := i
			for i < len(src) {
				r, size := utf8.DecodeRune(src[i:])
				if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
					break
				}

				i += size
			}

			if i == start {
				i++
				continue
			}

			result = append(result, token{kind: tokenIdent, text: string(src[start:i]), line: line, start: start, end: i})
		default:
			text := string(c)
			for _, operator := range operators {
				if strings.HasPrefix(string(src[i:min(i+len(operator), len(src))]), operator) {
					text = operator
					break
				}
			}

			kind := tokenOperator
			if text == ";" {
				kind = tokenSemicolon
			}

			result = append(result, token{kind: kind, text: text, line: line, start: i, end: i + len(text)})
			i += len(text)
		}
	}

	insertSemicolon(len(src))
	return result
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isKeyword(text string) bool {
	switch text {
	case "break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough", "for", "func",
		"go", "goto", "if", "import", "interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var":
		return true
	}

	return false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
