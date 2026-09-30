package cml

import (
	"sort"
	"strconv"

	"github.com/smarty/treaty/internal/graph"
)

// FromGraph builds the dump form of a graph: one block per source file,
// declarations in source order, methods and fields nested under their type,
// reference edges as directives under their source, and the file's imports
// as @import directives under its header.
//
// Notes:
//   - A method declared in a different file from its type is nested under the
//     type and carries its own file in its location.
//   - A symbol with build variants lists each as @variant file:line, and an
//     edge found in a variant carries the variant's file.
//
// Parameters:
//   - g: the graph to print.
//
// Returns:
//   - result: a document in canonical order.
func FromGraph(g *graph.Graph) *Document {
	g.Normalize()
	edges := map[string][]graph.Edge{}
	for _, edge := range g.Edges {
		edges[edge.From] = append(edges[edge.From], edge)
	}

	children := map[string][]*graph.Symbol{}
	for _, symbol := range g.Symbols {
		if symbol.Parent != "" {
			key := graph.SymbolID(symbol.Module, symbol.Parent)
			children[key] = append(children[key], symbol)
		}
	}

	type fileKey struct{ language, path string }
	blocks := map[fileKey]*Block{}
	var keys []fileKey
	for _, module := range g.Modules {
		for _, file := range module.Files {
			key := fileKey{module.Language, file}
			if blocks[key] == nil {
				blocks[key] = &Block{Language: module.Language, Path: file}
				keys = append(keys, key)
			}
		}
	}

	for _, symbol := range g.Symbols {
		if symbol.Parent != "" && g.Symbol(graph.SymbolID(symbol.Module, symbol.Parent)) != nil {
			continue
		}

		module := g.Module(symbol.Module)
		block := blocks[fileKey{module.Language, symbol.File}]
		if block == nil {
			continue
		}

		block.Nodes = append(block.Nodes, symbolNode(symbol, symbol.File, children, edges))
	}

	for _, item := range g.Imports {
		module := g.Module(item.From)
		if block := blocks[fileKey{module.Language, item.File}]; block != nil {
			block.Directives = append(block.Directives, Directive{Name: ImportDirective, Args: []string{item.To, "@" + strconv.Itoa(item.Line)}})
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path != keys[j].path {
			return keys[i].path < keys[j].path
		}

		return keys[i].language < keys[j].language
	})

	result := &Document{}
	for _, key := range keys {
		result.Blocks = append(result.Blocks, blocks[key])
	}

	return result
}

func symbolNode(symbol *graph.Symbol, headerFile string, children map[string][]*graph.Symbol, edges map[string][]graph.Edge) *Node {
	node := &Node{Text: symbol.Signature, SourceLine: symbol.Line, Pointer: symbol.Pointer}
	if symbol.File != headerFile {
		node.File = symbol.File
	}

	for _, variant := range symbol.Variants {
		node.Directives = append(node.Directives, Directive{Name: VariantDirective, Args: []string{variant.File + ":" + strconv.Itoa(variant.Line)}})
	}

	for _, edge := range edges[symbol.ID] {
		at := "@" + strconv.Itoa(edge.Line)
		if edge.File != symbol.File {
			at = "@" + edge.File + ":" + strconv.Itoa(edge.Line)
		}

		node.Directives = append(node.Directives, Directive{Name: edge.Kind, Args: []string{edge.To, at}})
	}

	for _, field := range symbol.Fields {
		node.Children = append(node.Children, &Node{Text: field.Text})
	}

	nested := children[symbol.ID]
	sort.SliceStable(nested, func(i, j int) bool {
		a, b := nested[i], nested[j]
		if (a.File == symbol.File) != (b.File == symbol.File) {
			return a.File == symbol.File
		}

		if a.File != b.File {
			return a.File < b.File
		}

		return a.Line < b.Line
	})

	for _, child := range nested {
		node.Children = append(node.Children, symbolNode(child, symbol.File, children, edges))
	}

	return node
}
