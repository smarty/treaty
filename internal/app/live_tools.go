package app

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/autopen"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

const findLimit = 50

var ErrEmptyPlan = errors.New("a plan needs at least one file or module block")

// Allowed says whether one module may depend on another, before the import
// is written.
//
// Parameters:
//   - from: the depending module, as an id such as go:internal/app, a path,
//     or a short name such as app; a symbol or file stands for its module.
//     A path that matches nothing is a module not built yet.
//   - to: the module depended on, given the same ways.
//
// Returns:
//   - result: the verdict and the rule behind it, one line each.
//   - err: the live graph is not built, or a name is ambiguous.
//
// Errors:
//   - ErrAmbiguousTarget: a short name matches more than one thing.
func (this *Live) Allowed(from, to string) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}
	return current.allowed(from, to)
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
			file, line, _ := violation.First()
			newViolations = append(newViolations, fmt.Sprintf("%s (%s; first at %s:%d)", key, violation.Rule, file, line))
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
	return current.find(query, kind), nil
}

// Impact lists everything that depends on a symbol or module, transitively:
// what an edit to it can break.
//
// Parameters:
//   - target: a symbol id, module id or file, or a short name that matches
//     exactly one, such as Store.CreateBook or store.go.
//
// Returns:
//   - result: the dependents grouped by module, contracts first.
//   - err: the live graph is not built, or the target does not exist.
//
// Errors:
//   - ErrUnknownTarget: nothing matches the target.
//   - ErrAmbiguousTarget: several symbols, modules or files match it.
func (this *Live) Impact(target string) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}
	return current.impact(target)
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
	return current.overview(), nil
}

// Plan saves unimplemented AutoPen as a design and checks it against the live
// graph, so the plan shows on the map and fills in as code is written.
//
// Notes:
//   - A plan replaces any design with the same name.
//   - Text without the "autopen 1" line gets a header naming the design.
//
// Parameters:
//   - name: the design name.
//   - text: AutoPen in design syntax.
//
// Returns:
//   - result: the design report: what is not built yet, what differs, and
//     what breaks layer rules.
//   - err: the text is not valid design AutoPen, or it could not be saved.
//
// Errors:
//   - ErrDumpSyntax: the text uses dump-only syntax.
//   - ErrEmptyPlan: the text declares nothing.
func (this *Live) Plan(name, text string) (result DesignReport, err error) {
	if trimmed := strings.TrimSpace(text); !strings.HasPrefix(trimmed, fmt.Sprintf("%s %d", autopen.Name, autopen.Version)) && !strings.HasPrefix(trimmed, fmt.Sprintf("cml %d", autopen.Version)) {
		text = fmt.Sprintf("%s %d\ndesign %q\n\n%s", autopen.Name, autopen.Version, name, text)
	}

	document, err := autopen.Parse(text)
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
	switch selection.Type {
	case "edge":
		target = selection.From
	case "file":
		// A file's id is its module id and path, joined by "|"; the slice is
		// its module's.
		target, _, _ = strings.Cut(selection.ID, "|")
	}

	if built, err := buildSlice(current, target); err == nil {
		slice = &built
	}

	return selection, slice, nil
}

// Slice builds a context slice from the live graph.
//
// Parameters:
//   - target: a symbol id, module id or file, or a short name that matches
//     exactly one.
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

// allowed says whether module from may depend on module to, and why.
func (this *analysis) allowed(from, to string) (result string, err error) {
	architecture := this.config.Architecture
	fromID, fromPlace, err := this.locate(from)
	if err != nil {
		return "", err
	}

	toID, toPlace, err := this.locate(to)
	if err != nil {
		return "", err
	}

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

// find lists the symbols whose id contains query, contracts first.
func (this *analysis) find(query, kind string) (result string) {
	type match struct {
		id, kind, file, signature string
		line                      int
		contract                  bool
	}

	needle := strings.ToLower(query)
	var matches []match
	for _, symbol := range this.head.Symbols {
		if (kind == "" || symbol.Kind == kind) && strings.Contains(strings.ToLower(symbol.ID), needle) {
			matches = append(matches, match{symbol.ID, symbol.Kind, symbol.File, symbol.Signature, symbol.Line, symbol.Contract})
		}

		// A struct's fields are found by name too, such as Query.Limit, at
		// their type's location.
		if kind != "" && kind != KindField {
			continue
		}

		for _, field := range symbol.Fields {
			for _, name := range fieldNames(field.Text) {
				if id := symbol.ID + "." + name; strings.Contains(strings.ToLower(id), needle) {
					matches = append(matches, match{id, KindField, symbol.File, field.Text, symbol.Line, field.Contract})
				}
			}
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].contract != matches[j].contract {
			return matches[i].contract
		}

		return matches[i].id < matches[j].id
	})

	var builder strings.Builder
	for index, found := range matches {
		if index == findLimit {
			fmt.Fprintf(&builder, "… %d more; narrow the query\n", len(matches)-findLimit)
			break
		}

		fmt.Fprintf(&builder, "%s  %s  %s:%d  %s\n", found.id, found.kind, found.file, found.line, found.signature)
	}

	if len(matches) == 0 {
		builder.WriteString("no symbol matches\n")
	}

	return builder.String()
}

// impact lists every transitive dependent of target, grouped by module.
func (this *analysis) impact(target string) (result string, err error) {
	g := this.head
	target, err = this.resolve(target)
	if err != nil {
		return "", err
	}

	var start []string
	switch {
	case g.Symbol(target) != nil:
		start = []string{target}
	case g.Module(target) != nil:
		for _, symbol := range g.SymbolsIn(target) {
			start = append(start, symbol.ID)
		}
	default:
		for _, symbol := range g.Symbols {
			if symbol.File == target {
				start = append(start, symbol.ID)
			}
		}
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
			if (g.Module(target) == nil || symbol.Module != target) && symbol.File != target {
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

// overview describes every module by layer and every module dependency.
func (this *analysis) overview() (result string) {
	g := this.head
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
	baseline := this.baseRef
	if baseline == "" {
		baseline = "none"
	}

	fmt.Fprintf(&builder, "%d modules, %d symbols, baseline %s\n", len(g.Modules), len(g.Symbols), baseline)
	builder.WriteString(this.config.Architecture.Summary() + "\n")
	for _, layer := range overviewOrder(this.config.Architecture, byLayer) {
		modules := byLayer[layer]
		if len(modules) == 0 {
			continue
		}

		fmt.Fprintf(&builder, "\n%s\n", layer)
		for _, module := range modules {
			metric := this.metrics[module.ID]
			fmt.Fprintf(&builder, "  %s  %d contracts  I=%.2f A=%.2f\n", module.ID, contracts[module.ID], metric.Instability, metric.Abstractness)
		}
	}

	builder.WriteString("\ndependencies (references)\n")
	violating := map[string]bool{}
	for _, violation := range this.violations {
		violating[violation.From+" "+violation.To] = true
	}

	for _, edge := range g.ModuleEdges() {
		marker := ""
		if violating[edge.From+" "+edge.To] {
			marker = "  VIOLATION"
		}

		imports := ""
		if len(edge.References) == 0 {
			imports = ", imports only"
		}

		fmt.Fprintf(&builder, "  %s → %s (%d%s)%s\n", edge.From, edge.To, len(edge.References), imports, marker)
	}

	if len(this.changes) > 0 {
		fmt.Fprintf(&builder, "\n%d contract change(s) since the baseline; call changes for detail\n", len(this.changes))
	}

	return builder.String()
}

// Source prints the code of a symbol, a file or a range of a file's lines
// in the live working tree.
//
// Parameters:
//   - target: as for Service.Source.
//
// Returns:
//   - result: the numbered lines under a header naming them.
//   - err: the live graph is not built, or the target is not a symbol, file
//     or valid range.
func (this *Live) Source(target string, all bool) (result string, err error) {
	current, err := this.current()
	if err != nil {
		return "", err
	}

	return this.service.sourceOf(current, target, all)
}

// Violations lists the live graph's rule violations, so a session can warn
// its agent about one the moment it appears.
//
// Returns:
//   - result: the violations, sorted by source and target.
//   - err: the live graph is not built.
func (this *Live) Violations() (result []rules.Violation, err error) {
	current, err := this.current()
	if err != nil {
		return nil, err
	}

	return current.violations, nil
}

// locate finds a module by id or path, and the placement it has or would
// have.
func (this *analysis) locate(module string) (id string, place rules.Placement, err error) {
	if found := this.head.Module(module); found != nil {
		return found.ID, placement(found), nil
	}

	for _, found := range this.head.Modules {
		if found.Path == module {
			return found.ID, placement(found), nil
		}
	}

	// A short name, or a symbol or file, stands for the module that holds
	// it. Only a name that matches nothing is a module not built yet.
	resolved, err := this.resolve(module)
	switch {
	case err == nil:
		if found := this.head.Module(resolved); found != nil {
			return found.ID, placement(found), nil
		}

		if symbol := this.head.Symbol(resolved); symbol != nil {
			found := this.head.Module(symbol.Module)
			return found.ID, placement(found), nil
		}

		if found, _ := fileTarget(this.head, resolved); found != nil {
			return found.ID, placement(found), nil
		}
	case errors.Is(err, ErrAmbiguousTarget):
		return "", rules.Placement{}, err
	}

	path := module
	if _, rest, ok := strings.Cut(module, ":"); ok {
		path = rest
	}

	return module + " (not built)", this.config.Architecture.Resolve(path), nil
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

// fieldNames reads the names a struct field line declares: one or more
// before the type, such as Limit or X, Y, or the type's own name for an
// embedded field.
func fieldNames(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	words := strings.Fields(text)
	if len(words) == 1 || strings.HasPrefix(words[1], "`") {
		embedded := strings.TrimPrefix(words[0], "*")
		return []string{embedded[strings.LastIndex(embedded, ".")+1:]}
	}

	var names []string
	for _, word := range words {
		name, more := strings.CutSuffix(word, ",")
		names = append(names, name)
		if !more {
			break
		}
	}

	return names
}
