package app

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/cml"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const findLimit = 50

var ErrEmptyPlan = errors.New("a plan needs at least one file or module block")

// Allowed says whether one module may depend on another, before the import
// is written.
//
// Parameters:
//   - from: the depending module, as an id such as go:internal/app or a path.
//   - to: the module depended on, as an id or a path.
//
// Returns:
//   - result: the verdict and the rule behind it, one line each.
//   - err: the live graph is not built.
func (this *Live) Allowed(from, to string) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	architecture := current.config.Architecture
	fromID, fromPlace := current.locate(from)
	toID, toPlace := current.locate(to)
	verdict, rule := "allowed", ""
	if fromID != toID {
		if allowed, broken := architecture.Check(fromPlace, toPlace); !allowed {
			verdict, rule = "not allowed", "rule: "+broken+"\n"
		}
	}

	return fmt.Sprintf("%s: %s (%s) → %s (%s)\n%s%s may depend on: %s\n",
		verdict, fromID, placeName(fromPlace), toID, placeName(toPlace), rule,
		placeName(fromPlace), strings.Join(architecture.MayUse(fromPlace), ", ")), nil
}

// Changes describes how the architecture differs from the baseline: contract
// changes, module edges gained and lost, and layer violations gained and
// lost.
//
// Returns:
//   - result: one line per change, grouped under headings.
//   - err: the live graph is not built.
func (this *Live) Changes() (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "baseline: %s\n", current.baseRef)
	if current.base == nil {
		builder.WriteString("nothing to compare with\n")
		return builder.String(), nil
	}

	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}

		fmt.Fprintf(&builder, "\n%s (%d)\n", title, len(lines))
		for _, line := range lines {
			builder.WriteString("  " + line + "\n")
		}
	}

	var contracts []string
	for _, change := range current.changes {
		line := fmt.Sprintf("%-14s %s", change.Kind, change.Symbol)
		if change.From != "" {
			line += " (was " + change.From + ")"
		}

		if detail := changeDetail(change); detail != "" {
			line += ": " + detail
		}

		contracts = append(contracts, line)
	}

	baseEdges, headEdges := moduleEdgeSet(current.base), moduleEdgeSet(current.head)
	var gained, lost []string
	for edge := range headEdges {
		if !baseEdges[edge] {
			gained = append(gained, edge)
		}
	}

	for edge := range baseEdges {
		if !headEdges[edge] {
			lost = append(lost, edge)
		}
	}

	baseViolations := map[string]bool{}
	for _, violation := range current.config.Architecture.Violations(current.base) {
		baseViolations[violation.From+" → "+violation.To] = true
	}

	var newViolations, fixed []string
	headViolations := map[string]bool{}
	for _, violation := range current.violations {
		key := violation.From + " → " + violation.To
		headViolations[key] = true
		if !baseViolations[key] {
			ref := violation.References[0]
			newViolations = append(newViolations, fmt.Sprintf("%s (%s; first at %s:%d)", key, violation.Rule, ref.File, ref.Line))
		}
	}

	for key := range baseViolations {
		if !headViolations[key] {
			fixed = append(fixed, key)
		}
	}

	sort.Strings(gained)
	sort.Strings(lost)
	sort.Strings(fixed)
	section("new layer violations", newViolations)
	section("fixed layer violations", fixed)
	section("contract changes", contracts)
	section("module dependencies added", gained)
	section("module dependencies removed", lost)
	if builder.Len() == len(fmt.Sprintf("baseline: %s\n", current.baseRef)) {
		builder.WriteString("no architectural change\n")
	}

	return builder.String(), nil
}

// Check runs every check on the live graph.
//
// Returns:
//   - result: the report, against the live baseline.
//   - err: the live graph is not built.
func (this *Live) Check() (result Report, err error) {
	current, err := this.current()
	if err != nil {
		return Report{}, err
	}

	return current.report(), nil
}

// DesignCheck compares a design with the live graph.
//
// Parameters:
//   - name: the design name.
//
// Returns:
//   - result: the design report.
//   - err: the live graph is not built, or the design cannot be read.
func (this *Live) DesignCheck(name string) (result DesignReport, err error) {
	current, err := this.current()
	if err != nil {
		return DesignReport{}, err
	}

	walk, err := this.service.checkDesign(current, name)
	if err != nil {
		return DesignReport{}, err
	}

	return *walk.report, nil
}

// Find lists symbols whose name or id contains the query, ignoring case.
//
// Parameters:
//   - query: the text to look for.
//   - kind: function, method, interface, type or value; empty for any.
//
// Returns:
//   - result: one line per symbol, id, kind, location and signature; at most
//     findLimit lines, contracts first.
//   - err: the live graph is not built.
func (this *Live) Find(query, kind string) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	needle := strings.ToLower(query)
	var matches []*graph.Symbol
	for _, symbol := range current.head.Symbols {
		if (kind == "" || symbol.Kind == kind) && strings.Contains(strings.ToLower(symbol.ID), needle) {
			matches = append(matches, symbol)
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Contract != matches[j].Contract {
			return matches[i].Contract
		}

		return matches[i].ID < matches[j].ID
	})

	var builder strings.Builder
	for index, symbol := range matches {
		if index == findLimit {
			fmt.Fprintf(&builder, "… %d more; narrow the query\n", len(matches)-findLimit)
			break
		}

		fmt.Fprintf(&builder, "%s  %s  %s:%d  %s\n", symbol.ID, symbol.Kind, symbol.File, symbol.Line, symbol.Signature)
	}

	if len(matches) == 0 {
		builder.WriteString("no symbol matches\n")
	}

	return builder.String(), nil
}

// Impact lists everything that depends on a symbol or module, transitively:
// what an edit to it can break.
//
// Parameters:
//   - target: a symbol id or module id.
//
// Returns:
//   - result: the dependents grouped by module, contracts first.
//   - err: the live graph is not built, or the target does not exist.
//
// Errors:
//   - ErrUnknownTarget: no symbol or module has that id.
func (this *Live) Impact(target string) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	g := current.head
	var start []string
	switch {
	case g.Symbol(target) != nil:
		start = []string{target}
	case g.Module(target) != nil:
		for _, symbol := range g.SymbolsIn(target) {
			start = append(start, symbol.ID)
		}
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownTarget, target)
	}

	callers := map[string][]string{}
	for _, edge := range g.Edges {
		callers[edge.To] = append(callers[edge.To], edge.From)
	}

	seen := map[string]bool{}
	for _, id := range start {
		seen[id] = true
	}

	queue := append([]string(nil), start...)
	byModule := map[string][]*graph.Symbol{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, caller := range callers[id] {
			if seen[caller] {
				continue
			}

			seen[caller] = true
			queue = append(queue, caller)
			symbol := g.Symbol(caller)
			if g.Module(target) == nil || symbol.Module != target {
				byModule[symbol.Module] = append(byModule[symbol.Module], symbol)
			}
		}
	}

	modules := make([]string, 0, len(byModule))
	total, contracts := 0, 0
	for module, symbols := range byModule {
		modules = append(modules, module)
		total += len(symbols)
		for _, symbol := range symbols {
			if symbol.Contract {
				contracts++
			}
		}
	}

	sort.Strings(modules)
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s: %d dependent symbol(s), %d of them contracts, in %d module(s)\n", target, total, contracts, len(modules))
	for _, module := range modules {
		symbols := byModule[module]
		sort.Slice(symbols, func(i, j int) bool {
			if symbols[i].Contract != symbols[j].Contract {
				return symbols[i].Contract
			}

			return symbols[i].ID < symbols[j].ID
		})

		fmt.Fprintf(&builder, "\n%s (%s)\n", module, placeName(placeOf(g, module)))
		for _, symbol := range symbols {
			_, name := graph.SplitSymbolID(symbol.ID)
			marker := " "
			if symbol.Contract {
				marker = "*"
			}

			fmt.Fprintf(&builder, " %s %s  %s:%d\n", marker, name, symbol.File, symbol.Line)
		}
	}

	return builder.String(), nil
}

// Overview describes the whole architecture compactly: every module by
// layer with its contract count, and every module dependency. It is meant as
// an agent's first call, instead of exploring files.
//
// Returns:
//   - result: the overview text.
//   - err: the live graph is not built.
func (this *Live) Overview() (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	g := current.head
	contracts := map[string]int{}
	for _, symbol := range g.Symbols {
		if symbol.Contract {
			contracts[symbol.Module]++
		}
	}

	byLayer := map[string][]*graph.Module{}
	for _, module := range g.Modules {
		key := placeName(placement(module))
		byLayer[key] = append(byLayer[key], module)
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "%d modules, %d symbols, baseline %s\n", len(g.Modules), len(g.Symbols), current.baseRef)
	builder.WriteString(current.config.Architecture.Summary() + "\n")
	for _, layer := range overviewOrder(current.config.Architecture, byLayer) {
		modules := byLayer[layer]
		if len(modules) == 0 {
			continue
		}

		fmt.Fprintf(&builder, "\n%s\n", layer)
		for _, module := range modules {
			metric := current.metrics[module.ID]
			fmt.Fprintf(&builder, "  %s  %d contracts  I=%.2f A=%.2f\n", module.ID, contracts[module.ID], metric.Instability, metric.Abstractness)
		}
	}

	builder.WriteString("\ndependencies (references)\n")
	violating := map[string]bool{}
	for _, violation := range current.violations {
		violating[violation.From+" "+violation.To] = true
	}

	for _, edge := range g.ModuleEdges() {
		marker := ""
		if violating[edge.From+" "+edge.To] {
			marker = "  VIOLATION"
		}

		fmt.Fprintf(&builder, "  %s → %s (%d)%s\n", edge.From, edge.To, len(edge.References), marker)
	}

	if len(current.changes) > 0 {
		fmt.Fprintf(&builder, "\n%d contract change(s) since the baseline; call changes for detail\n", len(current.changes))
	}

	return builder.String(), nil
}

// Plan saves unimplemented CML as a design and checks it against the live
// graph, so the plan shows on the map and fills in as code is written.
//
// Notes:
//   - A plan replaces any design with the same name.
//   - Text without the "cml 1" line gets a header naming the design.
//
// Parameters:
//   - name: the design name.
//   - text: CML in design syntax.
//
// Returns:
//   - result: the design report: what is not built yet, what differs, and
//     what breaks layer rules.
//   - err: the text is not valid design CML, or it could not be saved.
//
// Errors:
//   - ErrDumpSyntax: the text uses dump-only syntax.
//   - ErrEmptyPlan: the text declares nothing.
func (this *Live) Plan(name, text string) (result DesignReport, err error) {
	if !strings.HasPrefix(strings.TrimSpace(text), fmt.Sprintf("cml %d", cml.Version)) {
		text = fmt.Sprintf("cml %d\ndesign %q\n\n%s", cml.Version, name, text)
	}

	document, err := cml.Parse(text)
	if err != nil {
		return DesignReport{}, err
	}

	if document.IsDump() {
		return DesignReport{}, ErrDumpSyntax
	}

	if len(document.Blocks) == 0 {
		return DesignReport{}, ErrEmptyPlan
	}

	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}

	if _, err := this.service.workspace.SaveDesign(name, text); err != nil {
		return DesignReport{}, err
	}

	if err := this.Refresh(); err != nil {
		return DesignReport{}, err
	}

	return this.DesignCheck(name)
}

// Selected returns what the person has selected on the map, with its slice.
//
// Returns:
//   - selection: the selection, empty when nothing is selected.
//   - slice: the selected symbol's or module's slice, nil otherwise.
//   - err: the live graph is not built.
func (this *Live) Selected() (selection Selection, slice *Slice, err error) {
	current, err := this.current()
	if err != nil {
		return Selection{}, nil, err
	}

	this.mutex.Lock()
	selection = this.selection
	this.mutex.Unlock()
	target := selection.ID
	if selection.Type == "edge" {
		target = selection.From
	}

	if built, err := buildSlice(current, target); err == nil {
		slice = &built
	}

	return selection, slice, nil
}

// Slice builds a context slice from the live graph.
//
// Parameters:
//   - target: a symbol id or module id.
//
// Returns:
//   - result: the slice.
//   - err: the live graph is not built, or the target does not exist.
func (this *Live) Slice(target string) (result Slice, err error) {
	current, err := this.current()
	if err != nil {
		return Slice{}, err
	}

	return buildSlice(current, target)
}

// locate finds a module by id or path, and the placement it has or would
// have.
func (this *analysis) locate(module string) (id string, place rules.Placement) {
	if found := this.head.Module(module); found != nil {
		return found.ID, placement(found)
	}

	for _, found := range this.head.Modules {
		if found.Path == module {
			return found.ID, placement(found)
		}
	}

	path := module
	if _, rest, ok := strings.Cut(module, ":"); ok {
		path = rest
	}

	return module + " (not built)", this.config.Architecture.Resolve(path)
}

func changeDetail(change rules.Change) string {
	var parts []string
	if change.Before != "" {
		parts = append(parts, change.Before+" → "+change.After)
	}

	if len(change.FieldsRemoved) > 0 {
		parts = append(parts, "fields removed: "+strings.Join(change.FieldsRemoved, "; "))
	}

	if len(change.FieldsAdded) > 0 {
		parts = append(parts, "fields added: "+strings.Join(change.FieldsAdded, "; "))
	}

	return strings.Join(parts, "; ")
}

// overviewOrder lists the overview's groups: hexagonal and clean from the
// center outward, layered from the top down, and slices and contexts by
// name, with composition, shared and unclassified around them.
func overviewOrder(architecture rules.Architecture, groups map[string][]*graph.Module) []string {
	if architecture.Style == rules.StyleHexagonal {
		return []string{"domain", "application", "adapter (driving)", "adapter (driven)", "composition", "unclassified"}
	}

	var middle []string
	for key := range groups {
		if key != graph.LayerComposition && key != graph.LayerShared && key != graph.LayerUnclassified {
			middle = append(middle, key)
		}
	}

	names := architecture.LayerNames()
	if architecture.Style == rules.StyleLayered {
		slices.Reverse(names)
	}

	sort.Slice(middle, func(i, j int) bool {
		a, b := slices.Index(names, middle[i]), slices.Index(names, middle[j])
		if a != b {
			return a < b
		}

		return middle[i] < middle[j]
	})

	return append(append([]string{graph.LayerComposition}, middle...), graph.LayerShared, graph.LayerUnclassified)
}

// placeName names a placement for people: its layer and side, its slice
// and the layer inside it, or its context and whether it is public.
func placeName(place rules.Placement) string {
	switch {
	case place.Slice == "":
		if place.Side != "" {
			return fmt.Sprintf("%s (%s)", place.Layer, place.Side)
		}

		return place.Layer
	case place.Layer == graph.LayerContext && place.Public:
		return fmt.Sprintf("context %s (public)", place.Slice)
	case place.Layer == graph.LayerContext:
		return "context " + place.Slice
	case place.Layer == graph.LayerSlice:
		return "slice " + place.Slice
	default:
		return fmt.Sprintf("slice %s: %s", place.Slice, place.Layer)
	}
}

func placeOf(g *graph.Graph, module string) rules.Placement {
	if found := g.Module(module); found != nil {
		return placement(found)
	}

	return rules.Placement{Layer: graph.LayerUnclassified}
}

func placement(module *graph.Module) rules.Placement {
	return rules.Placement{Layer: module.Layer, Side: module.Side, Slice: module.Slice, Public: module.Public}
}

func moduleEdgeSet(g *graph.Graph) map[string]bool {
	result := map[string]bool{}
	for _, edge := range g.ModuleEdges() {
		result[edge.From+" → "+edge.To] = true
	}

	return result
}
