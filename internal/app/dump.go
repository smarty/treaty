package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/cml"
	"github.com/smarty/treaty/internal/graph"
)

// Dump prints the graph of a tree in CML.
//
// Parameters:
//   - at: a git ref, or empty for the working tree.
//
// Returns:
//   - result: the CML text.
//   - err: the tree could not be read.
func (this *Service) Dump(at string) (result string, err error) {
	g, err := this.graphAt(at)
	if err != nil {
		return "", err
	}

	return cml.Print(cml.FromGraph(g)), nil
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
//   - text: CML produced by Dump.
//
// Returns:
//   - result: the graph.
//   - err: the text is not valid CML, or names an unknown language.
func (this *Service) ParseGraph(text string) (result *graph.Graph, err error) {
	document, err := cml.Parse(text)
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

		for _, node := range block.Nodes {
			if err := addNode(result, dialect, module, block.Path, node, nil, nil); err != nil {
				return nil, err
			}
		}
	}

	result.Normalize()
	return result, nil
}

func addNode(g *graph.Graph, dialect Dialect, module *graph.Module, file string, node *cml.Node, parent *graph.Symbol, parentDecl *Declaration) error {
	declaration, err := dialect.Declare(node.Text, parentDecl)
	if err != nil {
		return fmt.Errorf("cml:%d: %w", node.Line, err)
	}

	if declaration.Kind == KindField {
		if parent == nil {
			return fmt.Errorf("cml:%d: field outside a type", node.Line)
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
		if directive.Name == cml.VariantDirective {
			if len(directive.Args) != 1 {
				return fmt.Errorf("cml:%d: expected @variant <file>:<line>", directive.Line)
			}

			file, line, err := location(directive.Args[0], "")
			if err != nil || file == "" {
				return fmt.Errorf("cml:%d: bad variant %q", directive.Line, directive.Args[0])
			}

			symbol.Variants = append(symbol.Variants, graph.Variant{File: file, Line: line})
			continue
		}

		if !cml.EdgeDirectives[directive.Name] {
			continue
		}

		if len(directive.Args) != 2 || !strings.HasPrefix(directive.Args[1], "@") {
			return fmt.Errorf("cml:%d: expected @%s <target> @[file:]<line>", directive.Line, directive.Name)
		}

		file, line, err := location(strings.TrimPrefix(directive.Args[1], "@"), symbol.File)
		if err != nil {
			return fmt.Errorf("cml:%d: bad line %q", directive.Line, directive.Args[1])
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
