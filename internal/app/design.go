package app

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/smarty/treaty/internal/cml"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const (
	ItemDiffers      = "differs"
	ItemExpectation  = "expectation"
	ItemForbidden    = "forbidden"
	ItemLayer        = "layer"
	ItemMissing      = "missing"
	ItemUnclassified = "unclassified"
	ItemUnverified   = "unverified"
)

var ErrDumpSyntax = errors.New("design files may not use dump-only syntax")

// DesignItem is one thing design check reports.
type DesignItem struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	Line   int    `json:"line"`
}

// DesignReport is the output of treaty design check.
type DesignReport struct {
	Name     string       `json:"name"`
	Title    string       `json:"title"`
	Items    []DesignItem `json:"items"`
	Failures int          `json:"failures"`
}

// designElement is one resolved design declaration, used by the map.
type designElement struct {
	ID        string
	Module    string
	Kind      string
	Signature string
	Parent    string
	Built     bool
}

// designWalk carries the state of one design check.
type designWalk struct {
	service  *Service
	analysis *analysis
	report   *DesignReport
	elements []designElement
}

// DesignCheck compares a design in the workspace with the working tree.
//
// Parameters:
//   - name: the design name, without directory or extension.
//
// Returns:
//   - result: every missing, differing, layer-breaking, unclassified and
//     failing item; result.Failures counts the ones that fail the check.
//   - err: the design or the tree could not be read.
func (this *Service) DesignCheck(name string) (result DesignReport, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return DesignReport{}, err
	}

	walk, err := this.checkDesign(analysis, name)
	if err != nil {
		return DesignReport{}, err
	}

	return *walk.report, nil
}

// DesignNew scaffolds a design from the current contracts of the targets.
//
// Parameters:
//   - name: the design name, without directory or extension.
//   - from: symbol or module ids to copy; empty for a header-only design.
//
// Returns:
//   - path: where the design was written.
//   - err: a target does not exist, or the design already exists.
//
// Errors:
//   - ErrUnknownTarget: a target is neither a symbol nor a module.
func (this *Service) DesignNew(name string, from []string) (path string, err error) {
	g, err := this.extractor.Extract(this.root)
	if err != nil {
		return "", err
	}

	g.Normalize()
	chosen := map[string]bool{}
	for _, target := range from {
		switch {
		case g.Module(target) != nil:
			for _, symbol := range g.SymbolsIn(target) {
				chosen[symbol.ID] = symbol.Contract
			}
		case g.Symbol(target) != nil:
			symbol := g.Symbol(target)
			chosen[symbol.ID] = true
			for _, child := range g.SymbolsIn(symbol.Module) {
				if child.Parent == symbol.Name && child.Contract {
					chosen[child.ID] = true
				}
			}
		default:
			return "", fmt.Errorf("%w: %s", ErrUnknownTarget, target)
		}
	}

	scaffold := graph.New()
	for _, symbol := range g.Symbols {
		if !chosen[symbol.ID] {
			continue
		}

		module := g.Module(symbol.Module)
		scaffold.AddModule(&graph.Module{ID: module.ID, Language: module.Language, Path: module.Path})
		scaffold.Module(module.ID).Files = append(scaffold.Module(module.ID).Files, symbol.File)
		if symbol.Parent != "" && !chosen[graph.SymbolID(symbol.Module, symbol.Parent)] {
			parent := *g.Symbol(graph.SymbolID(symbol.Module, symbol.Parent))
			parent.Fields = contractFields(parent.Fields)
			scaffold.AddSymbol(&parent)
			chosen[parent.ID] = true
		}

		copied := *symbol
		copied.Fields = contractFields(symbol.Fields)
		scaffold.AddSymbol(&copied)
	}

	document := cml.FromGraph(scaffold)
	document.Design = name
	stripLocations(document)
	return this.workspace.CreateDesign(name, cml.Print(document))
}

func (this *Service) checkDesign(analysis *analysis, name string) (*designWalk, error) {
	text, err := this.workspace.ReadDesign(name)
	if err != nil {
		return nil, err
	}

	document, err := cml.Parse(text)
	if err != nil {
		return nil, err
	}

	if document.IsDump() {
		return nil, ErrDumpSyntax
	}

	walk := &designWalk{service: this, analysis: analysis, report: &DesignReport{Name: name, Title: document.Design, Items: []DesignItem{}}}
	for _, block := range document.Blocks {
		if err := walk.block(block); err != nil {
			return nil, err
		}
	}

	for _, item := range walk.report.Items {
		if item.Kind != ItemUnverified {
			walk.report.Failures++
		}
	}

	return walk, nil
}

func (this *designWalk) add(kind, target string, line int, format string, args ...any) {
	this.report.Items = append(this.report.Items, DesignItem{Kind: kind, Target: target, Line: line, Detail: fmt.Sprintf(format, args...)})
}

func (this *designWalk) block(block *cml.Block) error {
	dialect, err := this.service.dialect(block.Language)
	if err != nil {
		return err
	}

	modulePath := dialect.ModulePath(block.Path)
	moduleID := graph.ModuleID(block.Language, modulePath)
	layer := this.analysis.config.Architecture.Resolve(modulePath).Layer
	if layer == graph.LayerUnclassified {
		this.add(ItemUnclassified, moduleID, block.Line, "treaty.yaml places %s in no layer", moduleID)
	}

	for _, directive := range block.Directives {
		this.directive(directive, moduleID, "", layer)
	}

	for _, node := range block.Nodes {
		if err := this.node(dialect, moduleID, layer, node, nil, nil); err != nil {
			return err
		}
	}

	return nil
}

func (this *designWalk) directive(directive cml.Directive, moduleID, symbolID, layer string) {
	owner := moduleID
	if symbolID != "" {
		owner = symbolID
	}

	g := this.analysis.head
	switch directive.Name {
	case "depends":
		if len(directive.Args) != 1 {
			this.add(ItemExpectation, owner, directive.Line, "@depends takes one target")
			return
		}

		targetModule := directive.Args[0]
		if module, _ := graph.SplitSymbolID(targetModule); module != "" {
			targetModule = module
		}

		architecture := this.analysis.config.Architecture
		target := rules.Placement{Layer: graph.LayerUnclassified}
		if module := g.Module(targetModule); module != nil {
			target = placement(module)
		} else if _, path, ok := strings.Cut(targetModule, ":"); ok {
			target = architecture.Resolve(path)
		}

		_, fromPath, _ := strings.Cut(moduleID, ":")
		if allowed, rule := architecture.Check(architecture.Resolve(fromPath), target); targetModule != moduleID && !allowed {
			this.add(ItemLayer, owner, directive.Line, "%s may not depend on %s: %s", moduleID, directive.Args[0], rule)
		}
	case "forbid":
		if len(directive.Args) != 1 {
			this.add(ItemExpectation, owner, directive.Line, "@forbid takes one target")
			return
		}

		for _, edge := range g.Edges {
			from, to := g.Symbol(edge.From), g.Symbol(edge.To)
			if (symbolID == "" && from.Module != moduleID) || (symbolID != "" && edge.From != symbolID) || to.Module == moduleID {
				continue
			}

			if rules.Match(directive.Args[0], to.Module) || rules.Match(directive.Args[0], to.ID) {
				this.add(ItemForbidden, owner, directive.Line, "%s uses %s at %s:%d, forbidden by @forbid %s", edge.From, edge.To, edge.File, edge.Line, directive.Args[0])
			}
		}
	case "expect":
		this.expect(directive, owner, moduleID, symbolID)
	}
}

func (this *designWalk) expect(directive cml.Directive, owner, moduleID, symbolID string) {
	if len(directive.Args) != 3 {
		this.add(ItemExpectation, owner, directive.Line, "@expect takes <metric> <op> <number>")
		return
	}

	metricName, op := directive.Args[0], directive.Args[1]
	want, err := strconv.ParseFloat(directive.Args[2], 64)
	if err != nil || (op != ">=" && op != "<=") {
		this.add(ItemExpectation, owner, directive.Line, "@expect %s: bad comparison %s %s", metricName, op, directive.Args[2])
		return
	}

	if symbolID != "" {
		this.add(ItemExpectation, owner, directive.Line, "@expect %s applies to modules, not symbols", metricName)
		return
	}

	metric, built := this.analysis.metrics[moduleID]
	if !built {
		this.add(ItemUnverified, owner, directive.Line, "%s %s %.2f not checked: module not built yet", metricName, op, want)
		return
	}

	var got float64
	switch metricName {
	case "instability":
		got = metric.Instability
	case "abstractness":
		got = metric.Abstractness
	case "distance":
		got = metric.Distance
	default:
		this.add(ItemExpectation, owner, directive.Line, "unknown metric %s", metricName)
		return
	}

	if (op == ">=" && got < want) || (op == "<=" && got > want) {
		this.add(ItemExpectation, owner, directive.Line, "%s is %.2f, expected %s %.2f", metricName, got, op, want)
	}
}

func (this *designWalk) node(dialect Dialect, moduleID, layer string, node *cml.Node, parent *graph.Symbol, parentDecl *Declaration) error {
	declaration, err := dialect.Declare(node.Text, parentDecl)
	if err != nil {
		return fmt.Errorf("design line %d: %w", node.Line, err)
	}

	if declaration.Kind == KindField {
		if parent != nil && !hasField(dialect, parent, parentDecl, declaration.Key) {
			this.add(ItemMissing, parent.ID, node.Line, "field %q not built", node.Text)
		}

		return nil
	}

	name := declaration.Name
	parentName := ""
	if parentDecl != nil {
		parentName = parentDecl.Name
		name = parentDecl.Name + "." + declaration.Name
	}

	id := graph.SymbolID(moduleID, name)
	built := this.analysis.head.Symbol(id)
	this.elements = append(this.elements, designElement{ID: id, Module: moduleID, Kind: declaration.Kind, Signature: node.Text, Parent: parentName, Built: built != nil})
	switch {
	case built == nil:
		this.add(ItemMissing, id, node.Line, "not built: %s", node.Text)
	default:
		actual, err := dialect.Declare(built.Signature, parentDecl)
		if err == nil && actual.Key != declaration.Key {
			this.add(ItemDiffers, id, node.Line, "designed %q, built %q", node.Text, built.Signature)
		}
	}

	for _, directive := range node.Directives {
		this.directive(directive, moduleID, id, layer)
	}

	for _, child := range node.Children {
		if err := this.node(dialect, moduleID, layer, child, built, &declaration); err != nil {
			return err
		}
	}

	return nil
}

func contractFields(fields []graph.Field) []graph.Field {
	var result []graph.Field
	for _, field := range fields {
		if field.Contract {
			result = append(result, field)
		}
	}

	return result
}

func hasField(dialect Dialect, symbol *graph.Symbol, parentDecl *Declaration, key string) bool {
	for _, field := range symbol.Fields {
		if declaration, err := dialect.Declare(field.Text, parentDecl); err == nil && declaration.Key == key {
			return true
		}
	}

	return false
}

func stripLocations(document *cml.Document) {
	var strip func(nodes []*cml.Node)
	strip = func(nodes []*cml.Node) {
		for _, node := range nodes {
			node.SourceLine, node.File, node.Pointer, node.Directives = 0, "", false, nil
			strip(node.Children)
		}
	}

	for _, block := range document.Blocks {
		strip(block.Nodes)
	}

	sort.SliceStable(document.Blocks, func(i, j int) bool { return document.Blocks[i].Path < document.Blocks[j].Path })
}
