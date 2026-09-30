package app

import (
	"fmt"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

// MapEdge is one module-level dependency on the map. Rule is the rule a
// violation breaks.
type MapEdge struct {
	From       string       `json:"from"`
	To         string       `json:"to"`
	Count      int          `json:"count"`
	New        bool         `json:"new"`
	Violation  bool         `json:"violation"`
	Rule       string       `json:"rule,omitempty"`
	References []graph.Edge `json:"references"`
}

// MapModule is one module hexagon on the map. Section is the vertical slice
// or bounded context holding it, and Public marks a context's public API.
type MapModule struct {
	ID       string        `json:"id"`
	Path     string        `json:"path"`
	Label    string        `json:"label"`
	Language string        `json:"language"`
	Layer    string        `json:"layer"`
	Side     string        `json:"side,omitempty"`
	Section  string        `json:"section,omitempty"`
	Public   bool          `json:"public,omitempty"`
	New      bool          `json:"new"`
	Design   bool          `json:"design"`
	Files    []string      `json:"files"`
	Metrics  rules.Metric  `json:"metrics"`
	Before   *rules.Metric `json:"before,omitempty"`
	Guidance string        `json:"guidance"`
	Slice    Slice         `json:"slice"`
}

// MapSymbol is one symbol node on the map.
type MapSymbol struct {
	ID        string `json:"id"`
	Module    string `json:"module"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Contract  bool   `json:"contract"`
	Signature string `json:"signature"`
	Before    string `json:"before,omitempty"`
	Change    string `json:"change,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Source    string `json:"source,omitempty"`
	Design    bool   `json:"design"`
	Slice     *Slice `json:"slice,omitempty"`
}

// MapView is everything the renderer draws, built in the same run.
// Architecture is the style that picks the layout, and Layers its layer
// names, innermost or lowest first.
type MapView struct {
	Title        string          `json:"title"`
	Architecture string          `json:"architecture"`
	Layers       []string        `json:"layers"`
	Summary      string          `json:"summary"`
	Base         string          `json:"base,omitempty"`
	Modules      []MapModule     `json:"modules"`
	Symbols      []MapSymbol     `json:"symbols"`
	Edges        []MapEdge       `json:"edges"`
	Links        []graph.Edge    `json:"links"`
	Findings     []rules.Finding `json:"findings"`
	Designs      []string        `json:"designs,omitempty"`
	Themes       []Theme         `json:"themes"`

	// Problems are designs that could not be overlaid, and other trouble the
	// live map reports without stopping.
	Problems []string `json:"problems,omitempty"`
}

// Map builds, scores and renders the working tree, with designs overlaid.
//
// Parameters:
//   - base: the git ref to diff against, or empty for no diff.
//   - designs: design names to overlay.
//
// Returns:
//   - path: where the HTML file was written.
//   - err: the tree, a design or the output could not be read or written.
func (this *Service) Map(base string, designs []string) (path string, err error) {
	analysis, err := this.analyze(base)
	if err != nil {
		return "", err
	}

	view, err := this.buildView(analysis, designs, false)
	if err != nil {
		return "", err
	}

	html, err := this.renderer.Render(view)
	if err != nil {
		return "", err
	}

	return this.workspace.WriteOutput("map.html", html)
}

// buildView turns an analysis into everything the map draws, with the named
// designs overlaid. A tolerant build reports a broken design as a problem
// instead of failing.
func (this *Service) buildView(analysis *analysis, designs []string, tolerant bool) (MapView, error) {
	architecture := analysis.config.Architecture
	view := MapView{
		Title: "Treaty", Architecture: architecture.Style, Layers: architecture.LayerNames(), Summary: architecture.Summary(),
		Base: analysis.baseRef, Findings: analysis.findings, Designs: designs, Links: analysis.head.Edges,
	}

	themes, err := this.themes.Themes()
	view.Themes = themes
	if err != nil {
		view.Problems = append(view.Problems, err.Error())
	}

	violations := map[[2]string]string{}
	for _, violation := range analysis.violations {
		violations[[2]string{violation.From, violation.To}] = violation.Rule
	}

	baseEdges := map[[2]string]bool{}
	if analysis.base != nil {
		for _, edge := range analysis.base.ModuleEdges() {
			baseEdges[[2]string{edge.From, edge.To}] = true
		}
	}

	for _, edge := range analysis.head.ModuleEdges() {
		key := [2]string{edge.From, edge.To}
		view.Edges = append(view.Edges, MapEdge{
			From: edge.From, To: edge.To, Count: len(edge.References), References: edge.References,
			Violation: violations[key] != "", Rule: violations[key], New: analysis.base != nil && !baseEdges[key],
		})
	}

	for _, module := range analysis.head.Modules {
		entry := MapModule{
			ID: module.ID, Path: module.Path, Label: label(module), Language: module.Language, Layer: module.Layer, Side: module.Side,
			Section: module.Slice, Public: module.Public,
			Files: module.Files, Metrics: analysis.metrics[module.ID], Guidance: architecture.Guidance(placement(module)),
			New: analysis.base != nil && analysis.base.Module(module.ID) == nil,
		}

		if before, ok := analysis.baseMetrics[module.ID]; ok {
			entry.Before = &before
		}

		entry.Slice, _ = buildSlice(analysis, module.ID)
		view.Modules = append(view.Modules, entry)
	}

	changes := map[string]rules.Change{}
	for _, change := range analysis.changes {
		changes[change.Symbol] = change
	}

	sources := map[string][]string{}
	for _, symbol := range analysis.head.Symbols {
		entry := MapSymbol{
			ID: symbol.ID, Module: symbol.Module, Name: symbol.Name, Kind: symbol.Kind, Contract: symbol.Contract,
			Signature: symbol.Signature, File: symbol.File, Line: symbol.Line,
			Change: changes[symbol.ID].Kind, Before: changes[symbol.ID].Before,
		}

		entry.Source = this.source(sources, symbol)
		if slice, err := buildSlice(analysis, symbol.ID); err == nil {
			entry.Slice = &slice
		}

		view.Symbols = append(view.Symbols, entry)
	}

	for _, name := range designs {
		walk, err := this.checkDesign(analysis, name)
		if err != nil && tolerant {
			view.Problems = append(view.Problems, fmt.Sprintf("design %s: %v", name, err))
			continue
		}

		if err != nil {
			return MapView{}, err
		}

		for _, element := range walk.elements {
			if element.Built {
				continue
			}

			if analysis.head.Module(element.Module) == nil && !hasModule(view.Modules, element.Module) {
				language, modulePath, _ := strings.Cut(element.Module, ":")
				place := architecture.Resolve(modulePath)
				view.Modules = append(view.Modules, MapModule{
					ID: element.Module, Path: modulePath, Label: modulePath, Language: language, Layer: place.Layer, Side: place.Side,
					Section: place.Slice, Public: place.Public, Design: true, Guidance: architecture.Guidance(place),
				})
			}

			_, name := graph.SplitSymbolID(element.ID)
			view.Symbols = append(view.Symbols, MapSymbol{ID: element.ID, Module: element.Module, Name: name, Kind: element.Kind, Contract: true, Signature: element.Signature, Design: true})
		}
	}

	return view, nil
}

func (this *Service) source(cache map[string][]string, symbol *graph.Symbol) string {
	lines, ok := cache[symbol.File]
	if !ok {
		data, err := this.extractor.Source(this.root, symbol.File)
		if err == nil {
			lines = strings.Split(string(data), "\n")
		}

		cache[symbol.File] = lines
	}

	end := max(symbol.EndLine, symbol.Line)
	if symbol.Line < 1 || end > len(lines) || end-symbol.Line > 200 {
		return ""
	}

	return strings.Join(lines[symbol.Line-1:end], "\n")
}

// label names a module for people: its path, or for the repository root,
// whose path is ".", the name its language gives it.
func label(module *graph.Module) string {
	if module.Path == "." && module.Name != "" {
		return module.Name + " (root)"
	}

	return module.Path
}

func hasModule(modules []MapModule, id string) bool {
	for _, module := range modules {
		if module.ID == id {
			return true
		}
	}

	return false
}
