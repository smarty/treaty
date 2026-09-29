// Package cml reads and writes the text form of CML: headers, indented
// declarations and @ directives. It knows nothing about any language; the
// text of each declaration is opaque to it.
package cml

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const Version = 1

var (
	// DesignDirectives may appear in design files.
	DesignDirectives = map[string]bool{"depends": true, "expect": true, "forbid": true}

	// EdgeDirectives appear only in dump output.
	EdgeDirectives = map[string]bool{"call": true, "embeds": true, "implements": true, "type-use": true}

	// VariantDirective marks another declaration of a symbol, built under
	// different constraints. It appears only in dump output.
	VariantDirective = "variant"

	locationToken = regexp.MustCompile(`^@(?:([^\s@:]+):)?(\d+)$`)
)

// Block is one header and everything nested beneath it.
type Block struct {
	Language   string
	Path       string
	Line       int
	Nodes      []*Node
	Directives []Directive
}

// Directive is one @ line, split into its name and arguments.
type Directive struct {
	Name string
	Args []string
	Line int
}

// Document is a parsed CML file.
type Document struct {
	Design string
	Blocks []*Block
}

// Error is a parse error with its line number.
type Error struct {
	Line    int
	Message string
}

// Node is one declaration line and everything nested beneath it.
type Node struct {
	Text       string
	Line       int
	File       string
	SourceLine int
	Pointer    bool
	Children   []*Node
	Directives []Directive
}

// Error formats the parse error.
//
// Returns:
//   - result: the message with its line number.
func (this *Error) Error() string {
	return fmt.Sprintf("cml:%d: %s", this.Line, this.Message)
}

// IsDump reports whether the document uses dump-only syntax anywhere.
//
// Returns:
//   - result: true when any node has a location, pointer marker or edge.
func (this *Document) IsDump() bool {
	for _, block := range this.Blocks {
		if nodesUseDumpSyntax(block.Nodes) {
			return true
		}
	}

	return false
}

// Parse reads a CML document.
//
// Parameters:
//   - text: the document text.
//
// Returns:
//   - result: the parsed document.
//   - err: the first syntax error.
//
// Errors:
//   - *Error: a malformed line, with its line number.
func Parse(text string) (result *Document, err error) {
	result = &Document{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var block *Block
	var stack []*Node
	sawVersion := false
	for index, raw := range lines {
		number := index + 1
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		if strings.Contains(raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))], "\t") {
			return nil, &Error{number, "indent with spaces, not tabs"}
		}

		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent%2 != 0 {
			return nil, &Error{number, "indentation must be a multiple of two spaces"}
		}

		depth := indent / 2
		switch {
		case !sawVersion:
			if trimmed != fmt.Sprintf("cml %d", Version) {
				return nil, &Error{number, fmt.Sprintf("expected \"cml %d\" as the first line", Version)}
			}

			sawVersion = true
		case depth == 0 && strings.HasPrefix(trimmed, "design "):
			title, unquoteErr := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "design ")))
			if unquoteErr != nil {
				return nil, &Error{number, "design title must be a quoted string"}
			}

			result.Design = title
		case depth == 0:
			language, path, ok := strings.Cut(trimmed, ":")
			if !ok || language == "" || path == "" || strings.ContainsAny(path, " \t") {
				return nil, &Error{number, "a header must be <lang>:<path>"}
			}

			block = &Block{Language: language, Path: path, Line: number}
			result.Blocks = append(result.Blocks, block)
			stack = stack[:0]
		case block == nil:
			return nil, &Error{number, "indented line before any header"}
		case strings.HasPrefix(trimmed, "@"):
			directive, directiveErr := parseDirective(trimmed, number)
			if directiveErr != nil {
				return nil, directiveErr
			}

			if depth == 1 {
				block.Directives = append(block.Directives, directive)
				break
			}

			if depth-2 >= len(stack) {
				return nil, &Error{number, "directive is nested too deeply"}
			}

			owner := stack[depth-2]
			owner.Directives = append(owner.Directives, directive)
		default:
			if depth-1 > len(stack) {
				return nil, &Error{number, "declaration is nested too deeply"}
			}

			node := parseNode(trimmed, number)
			stack = stack[:depth-1]
			if depth == 1 {
				block.Nodes = append(block.Nodes, node)
			} else {
				parent := stack[depth-2]
				parent.Children = append(parent.Children, node)
			}

			stack = append(stack, node)
		}
	}

	if !sawVersion {
		return nil, &Error{1, fmt.Sprintf("expected \"cml %d\" as the first line", Version)}
	}

	return result, nil
}

// Print writes a document in canonical form.
//
// Parameters:
//   - document: the document to print.
//
// Returns:
//   - result: the CML text.
func Print(document *Document) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "cml %d\n", Version)
	if document.Design != "" {
		fmt.Fprintf(&builder, "design %s\n", strconv.Quote(document.Design))
	}

	for _, block := range document.Blocks {
		fmt.Fprintf(&builder, "\n%s:%s\n", block.Language, block.Path)
		printDirectives(&builder, block.Directives, 1)
		printNodes(&builder, block, block.Nodes, 1)
	}

	return builder.String()
}

func nodesUseDumpSyntax(nodes []*Node) bool {
	for _, node := range nodes {
		if node.SourceLine > 0 || node.Pointer || nodesUseDumpSyntax(node.Children) {
			return true
		}

		for _, directive := range node.Directives {
			if EdgeDirectives[directive.Name] || directive.Name == VariantDirective {
				return true
			}
		}
	}

	return false
}

func parseDirective(text string, number int) (Directive, error) {
	fields := strings.Fields(strings.TrimPrefix(text, "@"))
	if len(fields) == 0 {
		return Directive{}, &Error{number, "empty directive"}
	}

	name := fields[0]
	if !DesignDirectives[name] && !EdgeDirectives[name] && name != VariantDirective {
		return Directive{}, &Error{number, fmt.Sprintf("unknown directive @%s", name)}
	}

	return Directive{Name: name, Args: fields[1:], Line: number}, nil
}

func parseNode(text string, number int) *Node {
	node := &Node{Line: number}
	for {
		index := strings.LastIndexAny(text, " \t")
		if index < 0 {
			break
		}

		token := text[index+1:]
		if token == "@ptr" {
			node.Pointer = true
		} else if match := locationToken.FindStringSubmatch(token); match != nil {
			node.File = match[1]
			node.SourceLine, _ = strconv.Atoi(match[2])
		} else {
			break
		}

		text = strings.TrimRight(text[:index], " \t")
	}

	node.Text = text
	return node
}

func printDirectives(builder *strings.Builder, directives []Directive, depth int) {
	for _, directive := range directives {
		fmt.Fprintf(builder, "%s@%s", strings.Repeat("  ", depth), directive.Name)
		for _, arg := range directive.Args {
			builder.WriteString(" ")
			builder.WriteString(arg)
		}

		builder.WriteString("\n")
	}
}

func printNodes(builder *strings.Builder, block *Block, nodes []*Node, depth int) {
	for _, node := range nodes {
		builder.WriteString(strings.Repeat("  ", depth))
		builder.WriteString(node.Text)
		if node.SourceLine > 0 {
			if node.File != "" {
				fmt.Fprintf(builder, " @%s:%d", node.File, node.SourceLine)
			} else {
				fmt.Fprintf(builder, " @%d", node.SourceLine)
			}
		}

		if node.Pointer {
			builder.WriteString(" @ptr")
		}

		builder.WriteString("\n")
		printDirectives(builder, node.Directives, depth+1)
		printNodes(builder, block, node.Children, depth+1)
	}
}
