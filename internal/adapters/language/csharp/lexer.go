// Package csharp reads C# source at the level of contracts: namespaces,
// using directives, type and member declarations with their signatures,
// plus the identifiers inside bodies that become reference edges. It is
// hand-written and does not parse expressions or statements; bodies are only
// scanned as tokens.
package csharp

import (
	"errors"
	"fmt"
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
	tokenComment
	tokenDoc
	tokenDirective
)

var ErrUnparsable = errors.New("cannot parse")

// operators lists the multi-character operators, longest first. A > never
// joins the next character, so the parser can close nested type arguments.
var operators = []string{
	"??=", "<<=",
	"::", "??", "?.", "=>", "==", "!=", "<=", "&&", "||", "++", "--", "->", "..",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<",
}

// token is one lexical token with its position in the source.
type token struct {
	kind  tokenKind
	text  string
	line  int
	start int
	end   int
}

// tokenKind classifies a token.
type tokenKind int

func (this tokenKind) String() string {
	switch this {
	case tokenIdent:
		return "ident"
	case tokenNumber:
		return "number"
	case tokenString:
		return "string"
	case tokenChar:
		return "char"
	case tokenOperator:
		return "op"
	case tokenComment:
		return "comment"
	case tokenDoc:
		return "doc"
	default:
		return "directive"
	}
}

// is reports whether the token is the given operator or identifier text.
func (this token) is(text string) bool {
	return (this.kind == tokenOperator || this.kind == tokenIdent) && this.text == text
}

// trivia reports whether the token is a comment or a preprocessor line,
// which declarations and signatures leave out.
func (this token) trivia() bool {
	return this.kind == tokenComment || this.kind == tokenDoc || this.kind == tokenDirective
}

// lexer holds the scan position.
type lexer struct {
	src  []byte
	i    int
	line int
}

// lex splits C# source into tokens and keeps the code of #if branches as
// section 4.6 of the design says.
//
// Errors:
//   - ErrUnparsable: a literal or comment does not end, the brackets are
//     not balanced, or an #if has no #endif.
func lex(src []byte) ([]token, error) {
	this := &lexer{src: src, line: 1}
	if strings.HasPrefix(string(src), "\ufeff") {
		this.i = len("\ufeff")
	}

	var result []token
	lineStart := true
	for this.i < len(src) {
		c := src[this.i]
		start, line := this.i, this.line
		switch {
		case c == '\n':
			this.line++
			this.i++
			lineStart = true
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			this.i++
			continue
		case c == '#' && lineStart:
			for this.i < len(src) && src[this.i] != '\n' {
				this.i++
			}

			text := strings.TrimSpace(string(src[start:this.i]))
			if comment := strings.Index(text, "//"); comment > 0 {
				text = strings.TrimSpace(text[:comment])
			}

			result = append(result, token{kind: tokenDirective, text: text, line: line, start: start, end: this.i})
			continue
		case c == '/' && this.peek(1) == '/':
			for this.i < len(src) && src[this.i] != '\n' {
				this.i++
			}

			kind := tokenComment
			if strings.HasPrefix(string(src[start:this.i]), "///") && !strings.HasPrefix(string(src[start:this.i]), "////") {
				kind = tokenDoc
			}

			result = append(result, token{kind: kind, text: strings.TrimRight(string(src[start:this.i]), "\r"), line: line, start: start, end: this.i})
		case c == '/' && this.peek(1) == '*':
			end := strings.Index(string(src[this.i+2:]), "*/")
			if end < 0 {
				return nil, this.fail(line, "a block comment does not end")
			}

			this.advance(this.i + 2 + end + 2)
			result = append(result, token{kind: tokenComment, text: string(src[start:this.i]), line: line, start: start, end: this.i})
		case c == '"' || c == '$' && this.stringAhead() || c == '@' && (this.peek(1) == '"' || this.peek(1) == '$' && this.peek(2) == '"'):
			if err := this.literal(); err != nil {
				return nil, err
			}

			if this.peek(0) == 'u' && this.peek(1) == '8' {
				this.i += 2
			}

			result = append(result, token{kind: tokenString, text: string(src[start:this.i]), line: line, start: start, end: this.i})
		case c == '\'':
			if err := this.char(); err != nil {
				return nil, err
			}

			result = append(result, token{kind: tokenChar, text: string(src[start:this.i]), line: line, start: start, end: this.i})
		case c == '@' || c == '_' || isLetter(this.rune()):
			this.i++
			for this.i < len(src) && isIdentPart(this.rune()) {
				_, size := utf8.DecodeRune(src[this.i:])
				this.i += size
			}

			result = append(result, token{kind: tokenIdent, text: string(src[start:this.i]), line: line, start: start, end: this.i})
		case c >= '0' && c <= '9' || c == '.' && this.peek(1) >= '0' && this.peek(1) <= '9':
			this.i++
			for this.i < len(src) {
				next := src[this.i]
				if next == '.' && this.peek(1) >= '0' && this.peek(1) <= '9' || next == '_' || next >= '0' && next <= '9' || next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' {
					this.i++
					continue
				}

				if (next == '+' || next == '-') && (src[this.i-1] == 'e' || src[this.i-1] == 'E') && !strings.HasPrefix(strings.ToLower(string(src[start:this.i])), "0x") {
					this.i++
					continue
				}

				break
			}

			result = append(result, token{kind: tokenNumber, text: string(src[start:this.i]), line: line, start: start, end: this.i})
		default:
			text := string(c)
			for _, operator := range operators {
				if strings.HasPrefix(string(src[this.i:min(this.i+len(operator), len(src))]), operator) {
					text = operator
					break
				}
			}

			if c >= utf8.RuneSelf {
				_, size := utf8.DecodeRune(src[this.i:])
				text = string(src[this.i : this.i+size])
			}

			this.i += len(text)
			result = append(result, token{kind: tokenOperator, text: text, line: line, start: start, end: this.i})
		}

		lineStart = false
	}

	result, err := branches(result)
	if err != nil {
		return nil, err
	}

	if err := balanced(result); err != nil {
		return nil, err
	}

	return result, nil
}

func (this *lexer) fail(line int, reason string) error {
	return fmt.Errorf("%w: line %d: %s", ErrUnparsable, line, reason)
}

func (this *lexer) peek(offset int) byte {
	if this.i+offset < len(this.src) {
		return this.src[this.i+offset]
	}

	return 0
}

func (this *lexer) rune() rune {
	r, _ := utf8.DecodeRune(this.src[this.i:])
	return r
}

// advance moves to a later position, counting the lines it passes.
func (this *lexer) advance(to int) {
	this.line += strings.Count(string(this.src[this.i:to]), "\n")
	this.i = to
}

// stringAhead reports whether a $ starts a string: $", $@" or $$…".
func (this *lexer) stringAhead() bool {
	at := this.i
	for at < len(this.src) && this.src[at] == '$' {
		at++
	}

	return at < len(this.src) && (this.src[at] == '"' || this.src[at] == '@' && at+1 < len(this.src) && this.src[at+1] == '"')
}

// literal scans one string literal of any form, starting at its prefix.
func (this *lexer) literal() error {
	line := this.line
	dollars, verbatim := 0, false
	for this.i < len(this.src) && (this.src[this.i] == '$' || this.src[this.i] == '@') {
		if this.src[this.i] == '$' {
			dollars++
		} else {
			verbatim = true
		}

		this.i++
	}

	quotes := 0
	for this.i+quotes < len(this.src) && this.src[this.i+quotes] == '"' {
		quotes++
	}

	if quotes >= 3 {
		closing := strings.Repeat(`"`, quotes)
		end := strings.Index(string(this.src[this.i+quotes:]), closing)
		if end < 0 {
			return this.fail(line, "a raw string does not end")
		}

		to := this.i + quotes + end + quotes
		for to < len(this.src) && this.src[to] == '"' {
			to++
		}

		this.advance(to)
		return nil
	}

	this.i++
	for this.i < len(this.src) {
		c := this.src[this.i]
		switch {
		case c == '"' && verbatim && this.peek(1) == '"':
			this.i += 2
		case c == '"':
			this.i++
			return nil
		case c == '\\' && !verbatim:
			this.i += 2
		case c == '\n' && !verbatim:
			return this.fail(line, "a string does not end on its line")
		case c == '{' && dollars > 0 && this.peek(1) == '{':
			this.i += 2
		case c == '{' && dollars > 0:
			if err := this.hole(line); err != nil {
				return err
			}
		case c == '\n':
			this.line++
			this.i++
		default:
			this.i++
		}
	}

	return this.fail(line, "a string does not end")
}

// hole scans an interpolation hole up to its closing brace, skipping the
// strings, characters and braces inside it.
func (this *lexer) hole(line int) error {
	depth := 0
	for this.i < len(this.src) {
		c := this.src[this.i]
		switch {
		case c == '{':
			depth++
			this.i++
		case c == '}':
			depth--
			this.i++
			if depth == 0 {
				return nil
			}
		case c == '"' || c == '$' && this.stringAhead() || c == '@' && this.peek(1) == '"':
			if err := this.literal(); err != nil {
				return err
			}
		case c == '\'':
			if err := this.char(); err != nil {
				return err
			}
		case c == '\n':
			this.line++
			this.i++
		default:
			this.i++
		}
	}

	return this.fail(line, "an interpolation does not end")
}

// char scans one character literal.
func (this *lexer) char() error {
	line := this.line
	this.i++
	for this.i < len(this.src) && this.src[this.i] != '\'' && this.src[this.i] != '\n' {
		if this.src[this.i] == '\\' {
			this.i++
		}

		this.i++
	}

	if this.i >= len(this.src) || this.src[this.i] != '\'' {
		return this.fail(line, "a character literal does not end")
	}

	this.i++
	return nil
}

// group is one #if group: the directive tokens at its start, each branch
// start and its end, as indexes into the token list.
type group struct {
	directives []int
}

// branches keeps the code of every branch of each #if group when each
// branch holds whole declarations, and only the first branch otherwise.
// The directive lines of the kept code stay in the list.
func branches(tokens []token) ([]token, error) {
	var stack []*group
	var groups []*group
	for index, current := range tokens {
		if current.kind != tokenDirective {
			continue
		}

		switch directive(current.text) {
		case "if":
			stack = append(stack, &group{directives: []int{index}})
		case "elif", "else":
			if len(stack) == 0 {
				return nil, fmt.Errorf("%w: line %d: %s without #if", ErrUnparsable, current.line, current.text)
			}

			top := stack[len(stack)-1]
			top.directives = append(top.directives, index)
		case "endif":
			if len(stack) == 0 {
				return nil, fmt.Errorf("%w: line %d: #endif without #if", ErrUnparsable, current.line)
			}

			top := stack[len(stack)-1]
			top.directives = append(top.directives, index)
			stack = stack[:len(stack)-1]
			groups = append(groups, top)
		}
	}

	if len(stack) > 0 {
		return nil, fmt.Errorf("%w: line %d: #if without #endif", ErrUnparsable, tokens[stack[0].directives[0]].line)
	}

	// Groups close inner first, so an outer group judges its branches after
	// the inner ones are settled.
	dropped := make([]bool, len(tokens))
	for _, each := range groups {
		if whole(tokens, dropped, each) {
			continue
		}

		last := each.directives[len(each.directives)-1]
		for index := each.directives[1]; index < last; index++ {
			dropped[index] = true
		}
	}

	var result []token
	for index, current := range tokens {
		if !dropped[index] {
			result = append(result, current)
		}
	}

	return result, nil
}

// whole reports whether every branch of a group holds whole declarations:
// the group starts where a declaration can, and each branch is balanced and
// ends with ; or }.
func whole(tokens []token, dropped []bool, each *group) bool {
	parens := 0
	previous := ""
	for index := 0; index < each.directives[0]; index++ {
		current := tokens[index]
		if dropped[index] || current.trivia() {
			continue
		}

		if current.kind == tokenOperator {
			switch current.text {
			case "(", "[":
				parens++
			case ")", "]":
				parens--
			}
		}

		previous = current.text
	}

	switch {
	case parens > 0:
		return false
	case previous != "" && previous != ";" && previous != "{" && previous != "}" && previous != "]":
		return false
	}

	for branch := 0; branch+1 < len(each.directives); branch++ {
		depth, last := 0, ""
		for index := each.directives[branch] + 1; index < each.directives[branch+1]; index++ {
			current := tokens[index]
			if dropped[index] || current.trivia() {
				continue
			}

			if current.kind == tokenOperator {
				switch current.text {
				case "(", "[", "{":
					depth++
				case ")", "]", "}":
					depth--
				}
			}

			if depth < 0 {
				return false
			}

			last = current.text
		}

		if depth != 0 || last != "" && last != ";" && last != "}" {
			return false
		}
	}

	return true
}

// directive names a preprocessor line's directive, such as if or endif.
func directive(text string) string {
	name := strings.TrimSpace(strings.TrimPrefix(text, "#"))
	if end := strings.IndexFunc(name, func(r rune) bool { return !unicode.IsLetter(r) }); end >= 0 {
		name = name[:end]
	}

	return name
}

// balanced reports an error when the brackets do not match.
func balanced(tokens []token) error {
	var open []token
	pairs := map[string]string{")": "(", "]": "[", "}": "{"}
	for _, current := range tokens {
		if current.kind != tokenOperator {
			continue
		}

		switch current.text {
		case "(", "[", "{":
			open = append(open, current)
		case ")", "]", "}":
			if len(open) == 0 || open[len(open)-1].text != pairs[current.text] {
				return fmt.Errorf("%w: line %d: unbalanced %s", ErrUnparsable, current.line, current.text)
			}

			open = open[:len(open)-1]
		}
	}

	if len(open) > 0 {
		return fmt.Errorf("%w: line %d: %s is not closed", ErrUnparsable, open[len(open)-1].line, open[len(open)-1].text)
	}

	return nil
}

func isLetter(r rune) bool {
	return unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Pc, r)
}
