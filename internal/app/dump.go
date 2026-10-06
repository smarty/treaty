package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/autopen"
	"github.com/smarty/treaty/internal/graph"
)

// Dump prints the graph of a tree in AutoPen.
//
// Parameters:
//   - at: a git ref, or empty for the working tree.
//
// Returns:
//   - result: the AutoPen text.
//   - err: the tree could not be read.
func (this *Service) Dump(at string) (result string, err error) {
	tree, err := this.graphAt(at)
	if err != nil {
		return "", err
	}

	return autopen.Print(autopen.FromGraph(tree.graph)), nil
}

// Extract builds the graph of the working tree, without layers.
//
// Returns:
//   - result: the graph.
//   - err: the tree could not be read.
func (this *Service) Extract() (result *graph.Graph, err error) {
	return this.extractor.Extract(this.root)
}

// ParseGraph reads dump output back into a graph.
//
// Parameters:
//   - text: AutoPen produced by Dump.
//
// Returns:
//   - result: the graph.
//   - err: the text is not valid AutoPen, or names an unknown language.
func (this *Service) ParseGraph(text string) (result *graph.Graph, err error) {
	document, err := autopen.Parse(text)
	if err != nil {
		return nil, err
	}

	result = graph.New()
	for _, block := range document.Blocks {
		dialect, err := this.dialect(block.Language)
		if err != nil {
			return nil, err
		}

		module := result.AddModule(&graph.Module{
			ID:       graph.ModuleID(block.Language, dialect.ModulePath(block.Path)),
			Language: block.Language,
			Path:     dialect.ModulePath(block.Path),
		})
		if dialect.IsFile(block.Path) {
			module.Files = append(module.Files, block.Path)
		}

		for _, directive := range block.Directives {
			if directive.Name != autopen.ImportDirective {
				continue
			}

			if len(directive.Args) != 2 || !strings.HasPrefix(directive.Args[1], "@") {
				return nil, fmt.Errorf("autopen:%d: expected @import <module> @<line>", directive.Line)
			}

			_, line, err := location(strings.TrimPrefix(directive.Args[1], "@"), block.Path)
			if err != nil {
				return nil, fmt.Errorf("autopen:%d: bad line %q", directive.Line, directive.Args[1])
			}

			result.AddImport(graph.Import{From: module.ID, To: directive.Args[0], File: block.Path, Line: line})
		}

		for _, node := range block.Nodes {
			if err := addNode(result, dialect, module, block.Path, node, nil, nil); err != nil {
				return nil, err
			}
		}
	}

	result.Normalize()
	return result, nil
}

func addNode(g *graph.Graph, dialect Dialect, module *graph.Module, file string, node *autopen.Node, parent *graph.Symbol, parentDecl *Declaration) error {
	declaration, err := dialect.Declare(node.Text, parentDecl)
	if err != nil {
		return fmt.Errorf("autopen:%d: %w", node.Line, err)
	}

	if declaration.Kind == KindField {
		if parent == nil {
			return fmt.Errorf("autopen:%d: field outside a type", node.Line)
		}

		parent.Fields = append(parent.Fields, graph.Field{Text: node.Text, Contract: declaration.Contract})
		return nil
	}

	symbol := &graph.Symbol{
		Name:      declaration.Name,
		Kind:      declaration.Kind,
		Module:    module.ID,
		File:      file,
		Line:      node.SourceLine,
		Contract:  declaration.Contract,
		Signature: node.Text,
		Pointer:   node.Pointer,
	}

	if node.File != "" {
		symbol.File = node.File
	}

	if parent != nil {
		symbol.Parent = parent.Name
		symbol.Name = parent.Name + "." + declaration.Name
	}

	symbol.ID = graph.SymbolID(module.ID, symbol.Name)
	g.AddSymbol(symbol)
	for _, directive := range node.Directives {
		if directive.Name == autopen.VariantDirective {
			if len(directive.Args) != 1 {
				return fmt.Errorf("autopen:%d: expected @variant <file>:<line>", directive.Line)
			}

			file, line, err := location(directive.Args[0], "")
			if err != nil || file == "" {
				return fmt.Errorf("autopen:%d: bad variant %q", directive.Line, directive.Args[0])
			}

			symbol.Variants = append(symbol.Variants, graph.Variant{File: file, Line: line})
			continue
		}

		if !autopen.EdgeDirectives[directive.Name] {
			continue
		}

		if len(directive.Args) != 2 || !strings.HasPrefix(directive.Args[1], "@") {
			return fmt.Errorf("autopen:%d: expected @%s <target> @[file:]<line>", directive.Line, directive.Name)
		}

		file, line, err := location(strings.TrimPrefix(directive.Args[1], "@"), symbol.File)
		if err != nil {
			return fmt.Errorf("autopen:%d: bad line %q", directive.Line, directive.Args[1])
		}

		g.AddEdge(graph.Edge{From: symbol.ID, To: directive.Args[0], Kind: directive.Name, File: file, Line: line})
	}

	for _, child := range node.Children {
		if err := addNode(g, dialect, module, file, child, symbol, &declaration); err != nil {
			return err
		}
	}

	return nil
}

// location reads "file:line" or "line", using fallback as the file when
// none is given.
func location(text, fallback string) (file string, line int, err error) {
	file = fallback
	if index := strings.LastIndex(text, ":"); index >= 0 {
		file, text = text[:index], text[index+1:]
	}

	line, err = strconv.Atoi(text)
	return file, line, err
}
