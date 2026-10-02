package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

// maxSourceBytes is the largest file the map carries for the inspector.
const maxSourceBytes = 1 << 20

// MapEdge is one module-level dependency on the map. Rule is the rule a
// violation breaks. Removed marks a dependency the base had and the working
// tree no longer has.
type MapEdge struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Count      int            `json:"count"`
	New        bool           `json:"new"`
	Removed    bool           `json:"removed,omitempty"`
	Violation  bool           `json:"violation"`
	Rule       string         `json:"rule,omitempty"`
	References []graph.Edge   `json:"references"`
	Imports    []graph.Import `json:"imports,omitempty"`
}

// MapModule is one module hexagon on the map. Section is the vertical slice
// or bounded context holding it, and Public marks a context's public API.
// Removed marks a module only the base has; RemovedFiles are files the base
// had in a module that is still there.
type MapModule struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Label    string   `json:"label"`
	Language string   `json:"language"`
	Layer    string   `json:"layer"`
	Side     string   `json:"side,omitempty"`
	Section  string   `json:"section,omitempty"`
	Public   bool     `json:"public,omitempty"`
	New      bool     `json:"new"`
	Removed  bool     `json:"removed,omitempty"`
	Design   bool     `json:"design"`
	Files    []string `json:"files"`

	RemovedFiles []string      `json:"removed_files,omitempty"`
	Manifest     string        `json:"manifest,omitempty"`
	Metrics      rules.Metric  `json:"metrics"`
	Before       *rules.Metric `json:"before,omitempty"`
	Guidance     string        `json:"guidance"`
	Slice        Slice         `json:"slice"`
}

// MapSymbol is one symbol node on the map. Diff is how its code changed
// since the base, line by line; Removed marks a symbol only the base has,
// drawn where it was.
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
	EndLine   int    `json:"end_line,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Design    bool   `json:"design"`
	Removed   bool   `json:"removed,omitempty"`
	Slice     *Slice `json:"slice,omitempty"`

	Diff []DiffLine `json:"diff,omitempty"`
}

// MapView is everything the renderer draws, built in the same run.
// Architecture is the style that picks the layout, and Layers its layer
// names, innermost or lowest first.
type MapView struct {
	Title        string       `json:"title"`
	Architecture string       `json:"architecture"`
	Layers       []string     `json:"layers"`
	Summary      string       `json:"summary"`
	Base         string       `json:"base,omitempty"`
	Modules      []MapModule  `json:"modules"`
	Symbols      []MapSymbol  `json:"symbols"`
	Edges        []MapEdge    `json:"edges"`
	Links        []graph.Edge `json:"links"`

	// RemovedLinks are references between symbols that the base had and the
	// working tree no longer has.
	RemovedLinks []graph.Edge `json:"removed_links,omitempty"`

	Findings []rules.Finding `json:"findings"`
	Designs  []string        `json:"designs,omitempty"`
	Themes   []Theme         `json:"themes"`

	// Sources holds each file's text, keyed by path, so the inspector can
	// show a whole file or cut a symbol's code from it. A file larger than
	// maxSourceBytes is left out.
	Sources map[string]string `json:"sources,omitempty"`

	// FileSlices holds each file's context slice, keyed by path, the same
	// slice an agent gets for the file.
	FileSlices map[string]Slice `json:"file_slices,omitempty"`

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
		count := len(edge.References)
		if count == 0 {
			count = len(edge.Imports)
		}

		if edge.References == nil {
			edge.References = []graph.Edge{}
		}

		view.Edges = append(view.Edges, MapEdge{
			From: edge.From, To: edge.To, Count: count, References: edge.References, Imports: edge.Imports,
			Violation: violations[key] != "", Rule: violations[key], New: analysis.base != nil && !baseEdges[key],
		})
	}

	for _, module := range analysis.head.Modules {
		entry := MapModule{
			ID: module.ID, Path: module.Path, Label: label(module), Language: module.Language, Layer: module.Layer, Side: module.Side,
			Section: module.Slice, Public: module.Public,
			Files: module.Files, Manifest: module.Manifest, Metrics: analysis.metrics[module.ID], Guidance: architecture.Guidance(placement(module)),
			New: analysis.base != nil && analysis.base.Module(module.ID) == nil,
		}

		if before, ok := analysis.baseMetrics[module.ID]; ok {
			entry.Before = &before
		}

		if analysis.base != nil {
			if old := analysis.base.Module(module.ID); old != nil {
				for _, file := range old.Files {
					if !slices.Contains(module.Files, file) {
						entry.RemovedFiles = append(entry.RemovedFiles, file)
					}
				}
			}
		}

		entry.Slice, _ = buildSlice(analysis, module.ID)
		view.Modules = append(view.Modules, entry)
		for _, file := range module.Files {
			if slice, err := buildSlice(analysis, file); err == nil {
				if view.FileSlices == nil {
					view.FileSlices = map[string]Slice{}
				}

				view.FileSlices[file] = slice
			}
		}
	}

	changes := map[string]rules.Change{}
	for _, change := range analysis.changes {
		changes[change.Symbol] = change
	}

	view.Sources = this.sources(analysis.head)
	for _, symbol := range analysis.head.Symbols {
		entry := MapSymbol{
			ID: symbol.ID, Module: symbol.Module, Name: symbol.Name, Kind: symbol.Kind, Contract: symbol.Contract,
			Signature: symbol.Signature, File: symbol.File, Line: symbol.Line, EndLine: symbol.EndLine, Doc: symbol.Doc,
			Change: changes[symbol.ID].Kind, Before: changes[symbol.ID].Before,
		}

		if slice, err := buildSlice(analysis, symbol.ID); err == nil {
			entry.Slice = &slice
		}

		entry.Diff = codeDiff(analysis, changes[symbol.ID], symbol, view.Sources)
		view.Symbols = append(view.Symbols, entry)
	}

	if analysis.base != nil {
		removed(analysis, &view, changes)
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

// sources reads the text of every file in the graph and every module's
// manifest, skipping files that cannot be read or are larger than
// maxSourceBytes.
func (this *Service) sources(g *graph.Graph) map[string]string {
	return this.sourcesAt(this.root, g)
}

// sourcesAt reads the text of a graph's files from the tree at root.
func (this *Service) sourcesAt(root string, g *graph.Graph) map[string]string {
	result := map[string]string{}
	for _, module := range g.Modules {
		files := module.Files
		if module.Manifest != "" {
			files = append([]string{module.Manifest}, files...)
		}

		for _, file := range files {
			if data, err := this.extractor.Source(root, file); err == nil && len(data) <= maxSourceBytes {
				result[file] = string(data)
			}
		}
	}

	return result
}

// codeDiff compares a changed symbol's code with its code in the base, or
// returns nil when nothing about its code changed.
func codeDiff(analysis *analysis, change rules.Change, symbol *graph.Symbol, sources map[string]string) []DiffLine {
	if analysis.base == nil || change.Kind == "" || change.Kind == rules.ChangeAdded {
		return nil
	}

	id := symbol.ID
	if change.From != "" {
		id = change.From
	}

	before := analysis.base.Symbol(id)
	if before == nil {
		return nil
	}

	old := linesOf(analysis.baseSources[before.File], before.Line, before.EndLine)
	now := linesOf(sources[symbol.File], symbol.Line, symbol.EndLine)
	if old == "" || old == now {
		return nil
	}

	return lineDiff(old, now)
}

// removed adds what the base had and the working tree does not, so the map
// can draw it where it was: modules, symbols, module dependencies and
// references. A symbol that moved is shown where it went, not as removed.
func removed(analysis *analysis, view *MapView, changes map[string]rules.Change) {
	base, head := analysis.base, analysis.head
	moved := map[string]bool{}
	for _, change := range changes {
		if change.From != "" {
			moved[change.From] = true
		}
	}

	architecture := analysis.config.Architecture
	for _, module := range base.Modules {
		if head.Module(module.ID) != nil {
			continue
		}

		view.Modules = append(view.Modules, MapModule{
			ID: module.ID, Path: module.Path, Label: label(module), Language: module.Language, Layer: module.Layer, Side: module.Side,
			Section: module.Slice, Public: module.Public, Files: module.Files, Manifest: module.Manifest, Removed: true,
			Metrics: analysis.baseMetrics[module.ID], Guidance: architecture.Guidance(placement(module)),
		})
	}

	for _, symbol := range base.Symbols {
		if head.Symbol(symbol.ID) != nil || moved[symbol.ID] {
			continue
		}

		entry := MapSymbol{
			ID: symbol.ID, Module: symbol.Module, Name: symbol.Name, Kind: symbol.Kind, Contract: symbol.Contract,
			Signature: symbol.Signature, File: symbol.File, Line: symbol.Line, EndLine: symbol.EndLine, Doc: symbol.Doc,
			Change: rules.ChangeRemoved, Removed: true,
		}

		if code := linesOf(analysis.baseSources[symbol.File], symbol.Line, symbol.EndLine); code != "" {
			entry.Diff = lineDiff(code, "")
		}

		view.Symbols = append(view.Symbols, entry)
	}

	kept := map[[2]string]bool{}
	for _, edge := range head.ModuleEdges() {
		kept[[2]string{edge.From, edge.To}] = true
	}

	for _, edge := range base.ModuleEdges() {
		if kept[[2]string{edge.From, edge.To}] {
			continue
		}

		count := len(edge.References)
		if count == 0 {
			count = len(edge.Imports)
		}

		if edge.References == nil {
			edge.References = []graph.Edge{}
		}

		view.Edges = append(view.Edges, MapEdge{From: edge.From, To: edge.To, Count: count, References: edge.References, Imports: edge.Imports, Removed: true})
	}

	links := map[[3]string]bool{}
	for _, edge := range head.Edges {
		links[[3]string{edge.From, edge.To, edge.Kind}] = true
	}

	for _, edge := range base.Edges {
		if !links[[3]string{edge.From, edge.To, edge.Kind}] {
			view.RemovedLinks = append(view.RemovedLinks, edge)
			links[[3]string{edge.From, edge.To, edge.Kind}] = true
		}
	}
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
