package rules

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const (
	// BreakAssembler is a snowflake dependency from a spoke on an assembler,
	// of its own star or any other.
	BreakAssembler = "assembler"

	// BreakContracts is a snowflake dependency from contracts on anything
	// but the contracts of their own star and the stars around it.
	BreakContracts = "contracts"

	// BreakEngine is a snowflake dependency from an engine on anything but
	// the contracts of its own star and the stars around it and the engines
	// of its own star.
	BreakEngine = "engine"

	// BreakIn is a snowflake dependency from an assembler into a branch
	// beyond the branch's core.
	BreakIn = "in"

	// BreakOut is a snowflake dependency on a star around the depending
	// module's own, past that star's contracts.
	BreakOut = "out"

	// BreakSideways is a snowflake dependency between sibling spokes.
	BreakSideways = "sideways"
)

// ErrMisfit is a snowflake whose stars do not fit the code: a star or an
// entry lies below a directory that holds a package, or an entry names a
// directory that holds none of its own.
var ErrMisfit = errors.New("the stars do not fit the code")

// Star is one star of the snowflake architecture: a directory relative to
// the repository root, such as . or internal/blocks, and the entries of its
// core region, its contracts, its assemblers and its engines. Each entry is
// a directory relative to the repository root naming exactly one package:
// the star's own, or one below the star's directory and outside its
// branches. A package beneath an entry is unclassified unless an entry names
// it too. Every other child of the star that holds a package is a spoke: a
// leaf, or a branch when it is a star of its own; the packages under a
// star's own directory are its spokes, whatever role its own package has.
//
// Notes:
//   - Contracts may use only the contracts of their own star and of the
//     stars around it.
//   - An engine runs policy over contracts: it may use the contracts of its
//     own star and of the stars around it, and its own star's engines.
//   - An assembler composes: it may use its own star's whole core and its
//     spokes, to wire them, the core of each child branch and the contracts
//     of the stars around it.
//   - A spoke may use its own leaf, its star's contracts and engines and
//     the contracts of every star around it, never an assembler.
//   - Deprecated marks a star whose treaty.yaml lists its assemblers under
//     orchestrators, the name v2 gave them.
type Star struct {
	Dir        string
	Contracts  []string
	Assemblers []string
	Engines    []string
	Deprecated bool
}

// StarNode is one star in the star tree, for people and the map: its
// directory, the star around it, how many stars lie above it, its entries
// exactly as treaty.yaml lists them, and its spokes. Leaves are the leaf
// directories that hold at least one module, and Branches the stars whose
// parent it is. The root star has no parent and depth 0.
type StarNode struct {
	Dir        string   `json:"dir"`
	Parent     string   `json:"parent,omitempty"`
	Depth      int      `json:"depth"`
	Contracts  []string `json:"contracts"`
	Assemblers []string `json:"assemblers"`
	Engines    []string `json:"engines"`
	Leaves     []string `json:"leaves"`
	Branches   []string `json:"branches"`
}

// starBinding is a snowflake bound to the modules of one tree: which
// directories hold a module, and which of those are misfits, looked through
// all the same.
type starBinding struct {
	modules  []string
	holds    map[string]bool
	misfits  map[string]bool
	problems []string
	shorts   []string
}

// Bind fits a snowflake architecture to the modules of one tree, so that
// placement can look through the directories that hold no module. Other
// styles return themselves.
//
// Notes:
//   - A misfit is a directory that holds a module but lies between a star
//     and its parent, or between a star and one of its entries; or an
//     entry that holds no module of its own while modules lie below it.
//   - The bound architecture is returned even with a misfit: its misfit
//     directories are looked through, and a module at one is
//     unclassified. A caller binding the working tree fails on the error;
//     one binding a base revision notes it and goes on.
//
// Parameters:
//   - modulePaths: the paths of the tree's modules.
//
// Returns:
//   - result: the bound architecture.
//   - err: the first misfit, in star order.
//
// Errors:
//   - ErrArchitecture and ErrMisfit, both: a star or an entry does not fit
//     the code.
func (this Architecture) Bind(modulePaths []string) (result Architecture, err error) {
	if this.Style != StyleSnowflake {
		return this, nil
	}

	binding := &starBinding{holds: map[string]bool{}, misfits: map[string]bool{}}
	for _, modulePath := range modulePaths {
		if !binding.holds[modulePath] {
			binding.holds[modulePath] = true
			binding.modules = append(binding.modules, modulePath)
		}
	}

	sort.Strings(binding.modules)
	stars := this.ordered()
	for _, star := range stars {
		if star.Dir == "." {
			continue
		}

		parent := this.Parent(star.Dir)
		if dir := binding.firstHolding(parent, star.Dir); dir != "" {
			binding.misfit(dir, fmt.Sprintf("star %s lies inside spoke %s of star %s", star.Dir, dir, parent),
				fmt.Sprintf("star %s lies inside spoke %s of star %s: %s holds a package, so it is a leaf holding everything below it; declare %s a star, or move its package into a child directory", star.Dir, dir, parent, dir, dir))
		}
	}

	for _, star := range stars {
		for _, role := range starRoles {
			for _, entry := range role.entries(star) {
				if entry == star.Dir {
					continue
				}

				if dir := binding.firstSpoke(this, star.Dir, entry); dir != "" {
					binding.misfit(dir, fmt.Sprintf("%s entry %s of star %s lies inside spoke %s", role.list, entry, star.Dir, dir),
						fmt.Sprintf("%s entry %s of star %s lies inside spoke %s: a core package is a child of its star or of another core package, never part of a spoke; move the package out of %s, or declare %s a star with %s in its %s", role.list, entry, star.Dir, dir, dir, dir, entry, role.list))
					continue
				}

				if below := binding.below(entry); !binding.holds[entry] && len(below) > 0 {
					short := fmt.Sprintf("%s entry %s of star %s holds no package of its own", role.list, entry, star.Dir)
					binding.problems = append(binding.problems, fmt.Sprintf("%s, only %s below it; name each package, so a new one stays a spoke until treaty.yaml names it", short, joinAnd(below)))
					binding.shorts = append(binding.shorts, short)
				}
			}
		}
	}

	result = this
	result.bound = binding
	if len(binding.problems) > 0 {
		return result, fmt.Errorf("%w: %w: %s", ErrArchitecture, ErrMisfit, binding.problems[0])
	}

	return result, nil
}

// Misfit names, for people, the first directory a bound snowflake looks
// through although it holds a package, such as star internal/blocks lies
// inside spoke internal of star .
//
// Returns:
//   - result: the misfit; empty when the stars fit the code, the
//     architecture is not bound, or the style is not snowflake.
func (this Architecture) Misfit() string {
	if this.bound == nil || len(this.bound.shorts) == 0 {
		return ""
	}

	return this.bound.shorts[0]
}

// Tree lists every star of a snowflake architecture with its entries,
// leaves and branches, the root first and each star before the stars
// inside it. Leaves are found among the modules it was bound to, so an
// unbound architecture lists none.
//
// Returns:
//   - result: one node per star; empty for any other style.
func (this Architecture) Tree() []StarNode {
	if this.Style != StyleSnowflake {
		return nil
	}

	leaves := map[string][]string{}
	if this.bound != nil {
		for _, modulePath := range this.bound.modules {
			placement := this.Resolve(modulePath)
			if placement.Layer == graph.LayerLeaf && !slices.Contains(leaves[placement.Star], placement.Spoke) {
				leaves[placement.Star] = append(leaves[placement.Star], placement.Spoke)
			}
		}
	}

	stars := this.ordered()
	result := make([]StarNode, 0, len(stars))
	for _, star := range stars {
		node := StarNode{Dir: star.Dir, Parent: this.Parent(star.Dir), Depth: len(this.ancestors(star.Dir)),
			Contracts: listOrEmpty(star.Contracts), Assemblers: listOrEmpty(star.Assemblers), Engines: listOrEmpty(star.Engines), Leaves: listOrEmpty(leaves[star.Dir]), Branches: this.branchesOf(star.Dir)}
		sort.Strings(node.Leaves)
		result = append(result, node)
	}

	return result
}

// Notes lists, for people, what a bound snowflake finds odd without
// refusing it: a star that still lists orchestrators, v2's name for its
// assemblers; a star that holds no module; an entry that names no module; a
// star's own package that no list names; a star whose core is empty while
// it has leaves; and a package beneath a core package that no entry names.
//
// Returns:
//   - result: one note each, in star order; empty for any other style.
func (this Architecture) Notes() (result []string) {
	if this.Style != StyleSnowflake {
		return nil
	}

	binding := this.binding("")
	hasLeaves := map[string]bool{}
	var nested []string
	for _, modulePath := range binding.modules {
		switch placement := this.Resolve(modulePath); {
		case placement.Layer == graph.LayerLeaf:
			hasLeaves[placement.Star] = true
		case placement.Layer == graph.LayerUnclassified && placement.Entry != "":
			nested = append(nested, modulePath)
		}
	}

	for _, star := range this.ordered() {
		if star.Deprecated {
			result = append(result, fmt.Sprintf("star %s lists orchestrators, the name the second snowflake gave its assemblers; rename the key to assemblers, and move each package that only runs policy over contracts to engines", star.Dir))
		}

		if !slices.ContainsFunc(binding.modules, func(modulePath string) bool { return encloses(star.Dir, modulePath) }) {
			result = append(result, fmt.Sprintf("treaty.yaml declares star %s under stars, but no module lies in it; check the directory's spelling", star.Dir))
			continue
		}

		for _, role := range starRoles {
			for _, entry := range role.entries(star) {
				if !binding.holds[entry] && (entry == star.Dir || len(binding.below(entry)) == 0) {
					result = append(result, fmt.Sprintf("%s entry %s of star %s matches no module; check its spelling", role.list, entry, star.Dir))
				}
			}
		}

		if binding.holds[star.Dir] && star.role(star.Dir) == nil {
			result = append(result, fmt.Sprintf("star %s holds a package of its own, %s, which treaty.yaml lists in none of its contracts, assemblers or engines, so it is unclassified; list it in one of them", star.Dir, star.Dir))
		}

		if len(star.Contracts)+len(star.Assemblers)+len(star.Engines) == 0 && hasLeaves[star.Dir] {
			result = append(result, fmt.Sprintf("star %s has no contracts, assemblers or engines, so nothing wires its spokes and they may use only themselves and the contracts of the stars around it", star.Dir))
		}
	}

	for _, modulePath := range nested {
		placement := this.Resolve(modulePath)
		result = append(result, fmt.Sprintf("%s lies beneath %s of star %s, and a core role covers only the package an entry names, so it is unclassified; list it under the contracts, assemblers or engines of star %s, or move it out of %s", modulePath, starName(this.claimed(placement)), placement.Star, placement.Star, placement.Entry))
	}

	return result
}

// Crossing names, for a dependency the snowflake refuses, the deepest star
// around both ends and the child of that star that holds the module
// depended on: its entry or leaf when it lies in that star, or else the
// branch of that star around it. A port for the dependency belongs in that
// star's contracts.
//
// Parameters:
//   - from: the depending module's placement.
//   - to: the placement of the module depended on.
//
// Returns:
//   - star: the deepest star around both.
//   - child: the child of star that holds to.
func (this Architecture) Crossing(from, to Placement) (star, child string) {
	star = this.common(from.Star, to.Star)
	return star, this.childOf(star, to)
}

// Handover words how a port carries a refused dependency of a snowflake:
// the contracts of the deepest star around both ends declare it, and the
// assemblers of the stars from there down to the depending module's hand
// the implementation in. The depending module uses the declaring contracts
// directly, wherever they lie around it: there are no re-exports.
//
// Parameters:
//   - from: the depending module's placement.
//   - to: the placement of the module depended on.
//
// Returns:
//   - declare: the contracts that declare the port, such as the contracts
//     of star . (internal/contracts).
//   - hands: who hands the implementation in, such as an assembler of star
//     . or the assemblers of stars . and internal/blocks.
func (this Architecture) Handover(from, to Placement) (declare, hands string) {
	common := this.common(from.Star, to.Star)
	carriers := this.between(common, from.Star)
	if from.Layer != graph.LayerAssembler && from.Star != common {
		carriers = append(carriers, from.Star)
	}

	if len(carriers) == 0 {
		return this.contractsOf(common), "an assembler of star " + common
	}

	return this.contractsOf(common), "the assemblers of stars " + joinAnd(append([]string{common}, carriers...))
}

// Encloses reports whether a star directory holds a path, or is that
// path. The repository root, ".", holds everything.
//
// Parameters:
//   - dir: the star's directory.
//   - inner: a module path or another star's directory.
//
// Returns:
//   - result: true when inner is dir or lies inside it.
func Encloses(dir, inner string) bool { return encloses(dir, inner) }

// Innermost names the deepest star of a snowflake architecture whose
// directory holds a path, or is it.
//
// Parameters:
//   - modulePath: the path relative to the repository root.
//
// Returns:
//   - result: the star's directory; empty when no star holds the path or
//     the style is not snowflake.
func (this Architecture) Innermost(modulePath string) string {
	if this.Style != StyleSnowflake {
		return ""
	}

	if star := this.innermost(modulePath); star != nil {
		return star.Dir
	}

	return ""
}

// Parent names the star around a star: the nearest listed star strictly
// around its directory.
//
// Parameters:
//   - dir: the star's directory.
//
// Returns:
//   - result: the parent's directory; empty for the root, or when no star
//     lies around it.
func (this Architecture) Parent(dir string) string {
	for dir != "." {
		dir = path.Dir(dir)
		if this.star(dir) != nil {
			return dir
		}
	}

	return ""
}

// Position names the child of its innermost star that holds a path: the
// shallowest directory on the way down to it that is an entry or holds a
// module, looking through the rest, or the path itself. A star's own
// package has the star's directory.
//
// Parameters:
//   - modulePath: the path relative to the repository root.
//
// Returns:
//   - result: the position; empty when no star holds the path or the style
//     is not snowflake.
func (this Architecture) Position(modulePath string) string {
	star := this.Innermost(modulePath)
	if star == "" || star == modulePath {
		return star
	}

	return this.position(this.binding(modulePath), star, modulePath)
}

// starRole is one of a star's three lists of entries.
type starRole struct {
	layer, list string
	entries     func(Star) []string
}

// starRoles are a star's lists of entries, in the order treaty.yaml lists
// them: contracts, assemblers, engines.
var starRoles = []starRole{
	{graph.LayerContracts, "contracts", func(star Star) []string { return star.Contracts }},
	{graph.LayerAssembler, "assemblers", func(star Star) []string { return star.Assemblers }},
	{graph.LayerEngine, "engines", func(star Star) []string { return star.Engines }},
}

// resolveSnowflake places one module path in the star tree:
//
//  1. its star is the innermost star whose directory holds it or is it;
//  2. a package an entry names exactly has that entry's role, and a star's
//     own package that no entry names is unclassified;
//  3. a misfit directory, in a binding that looks through it, is
//     unclassified;
//  4. a package beneath a core package, other than the star's own, that no
//     entry names is unclassified, with that core package as its Entry;
//  5. otherwise it is a leaf: its position in its star, nested packages
//     included.
func (this Architecture) resolveSnowflake(modulePath string) Placement {
	star := this.innermost(modulePath)
	if star == nil {
		return Placement{Layer: graph.LayerUnclassified}
	}

	if placement := star.claim(modulePath); placement.Layer != graph.LayerUnclassified || modulePath == star.Dir {
		return placement
	}

	binding := this.binding(modulePath)
	if binding.misfits[modulePath] {
		return Placement{Layer: graph.LayerUnclassified, Star: star.Dir, Spoke: modulePath}
	}

	position := this.position(binding, star.Dir, modulePath)
	if star.role(position) != nil {
		return Placement{Layer: graph.LayerUnclassified, Star: star.Dir, Entry: position}
	}

	return Placement{Layer: graph.LayerLeaf, Star: star.Dir, Spoke: position}
}

// claim places a directory a star's entry names exactly, or reports it
// unclassified when no list names it.
func (this Star) claim(dir string) Placement {
	if role := this.role(dir); role != nil {
		return Placement{Layer: role.layer, Star: this.Dir, Entry: dir}
	}

	return Placement{Layer: graph.LayerUnclassified, Star: this.Dir}
}

// role finds the list of a star that names a directory, or nil.
func (this Star) role(dir string) *starRole {
	for i, role := range starRoles {
		if slices.Contains(role.entries(this), dir) {
			return &starRoles[i]
		}
	}

	return nil
}

// claimed is the placement of the core package an unclassified placement
// lies beneath.
func (this Architecture) claimed(placement Placement) Placement {
	if star := this.star(placement.Star); star != nil {
		return star.claim(placement.Entry)
	}

	return placement
}

// Break names the kind of rule a dependency breaks in a snowflake.
//
// Parameters:
//   - from: the depending module's placement.
//   - to: the placement of the module depended on.
//
// Returns:
//   - result: BreakSideways, BreakOut, BreakIn, BreakContracts, BreakEngine
//     or BreakAssembler; empty when the dependency is allowed, either end is
//     unclassified, or the style is not snowflake.
func (this Architecture) Break(from, to Placement) string {
	if this.Style != StyleSnowflake || from.Layer == graph.LayerUnclassified || to.Layer == graph.LayerUnclassified {
		return ""
	}

	kind, _ := this.snowflakeBreak(from, to)
	return kind
}

// checkSnowflake applies the snowflake's rules between two placed modules,
// neither of them unclassified.
func (this Architecture) checkSnowflake(from, to Placement) (bool, string) {
	kind, rule := this.snowflakeBreak(from, to)
	return kind == "", rule
}

// Rule texts: each refusal names the rule it breaks.
const (
	ruleContracts = "contracts use only contracts of their own star and its ancestors"
	ruleEngine    = "an engine runs policy over contracts only: it uses the contracts of its own star and its ancestors and the engines of its own star"
	ruleAssembler = "a spoke never uses an assembler"
	ruleSideways  = "no dependency runs sideways"
)

// snowflakeBreak finds the kind of rule a dependency between two placed
// modules breaks, and the rule's text, or nothing when it is allowed.
func (this Architecture) snowflakeBreak(from, to Placement) (kind, rule string) {
	own, theirs := from.Star, to.Star
	around := encloses(theirs, own)
	switch from.Layer {
	case graph.LayerContracts:
		if to.Layer == graph.LayerContracts && around {
			return "", ""
		}

		return BreakContracts, this.contractsRule(from, to)
	case graph.LayerEngine:
		if to.Layer == graph.LayerContracts && around || to.Layer == graph.LayerEngine && theirs == own {
			return "", ""
		}

		return BreakEngine, this.portRule(from, to, ruleEngine)
	case graph.LayerAssembler:
		switch {
		case theirs == own, this.Parent(theirs) == own && isCore(to), to.Layer == graph.LayerContracts && around:
			return "", ""
		case above(own, theirs):
			return BreakIn, this.inRule(from, to)
		case isCore(to) && above(theirs, own):
			return BreakOut, this.portRule(from, to, "an assembler uses only the contracts of the stars around its own")
		}
	case graph.LayerLeaf:
		switch {
		case to.Layer == graph.LayerAssembler:
			return BreakAssembler, this.portRule(from, to, ruleAssembler+": an assembler hands a spoke what it needs")
		case theirs == own && (to.Layer == graph.LayerLeaf && to.Spoke == from.Spoke || to.Layer == graph.LayerContracts || to.Layer == graph.LayerEngine):
			return "", ""
		case to.Layer == graph.LayerContracts && around:
			return "", ""
		case to.Layer == graph.LayerEngine && above(theirs, own):
			return BreakOut, this.portRule(from, to, fmt.Sprintf("a spoke uses the engines of its own star, %s, and only the contracts of the stars around it", own))
		}
	default:
		return "", ""
	}

	return BreakSideways, this.sidewaysRule(from, to)
}

// contractsRule words a dependency of contracts on anything but the
// contracts of their own star and the stars around it.
func (this Architecture) contractsRule(from, to Placement) string {
	prefix := fmt.Sprintf("%s may not depend on %s: %s; ", starName(from), starName(to), ruleContracts)
	switch to.Layer {
	case graph.LayerContracts:
		return prefix + fmt.Sprintf("move what it needs into %s, which both may use", this.contractsOf(this.common(from.Star, to.Star)))
	case graph.LayerLeaf:
		return prefix + fmt.Sprintf("declare what it needs in contracts, and let an assembler hand %s in", to.Spoke)
	}

	return prefix + fmt.Sprintf("declare what it needs in contracts, and let %s implement it", to.Entry)
}

// portRule words a refused dependency that a port in the contracts of the
// deepest star around both ends would carry instead.
func (this Architecture) portRule(from, to Placement, broken string) string {
	declare, hands := this.Handover(from, to)
	return fmt.Sprintf("%s may not depend on %s: %s; declare a port in %s, implement it in %s, and let %s hand it in",
		starName(from), starName(to), broken, declare, this.childOf(this.common(from.Star, to.Star), to), hands)
}

// sidewaysRule words a dependency between modules under two different
// children of their common star.
func (this Architecture) sidewaysRule(from, to Placement) string {
	common := this.common(from.Star, to.Star)
	mine, theirs := this.childOf(common, from), this.childOf(common, to)
	suffix := ""
	switch {
	case from.Star != common && to.Star != common && isCore(from) && from.Star == mine && isCore(to) && to.Star == theirs:
		suffix = ", not even core to core"
	case to.Star != common && isCore(to) && to.Star == theirs:
		suffix = ", not even on a branch's core"
	}

	fix := "declare what it needs in " + this.contractsOf(common)
	if to.Layer != graph.LayerContracts {
		declare, hands := this.Handover(from, to)
		fix = fmt.Sprintf("declare a port in %s, implement it in %s, and let %s hand it in", declare, theirs, hands)
	}

	return fmt.Sprintf("%s may not depend on %s: %s and %s are sibling spokes of star %s, and %s%s; %s",
		starName(from), starName(to), mine, theirs, common, ruleSideways, suffix, fix)
}

// inRule words a dependency from an assembler into a branch of its star,
// beyond that branch's core.
func (this Architecture) inRule(from, to Placement) string {
	branch := this.childOf(from.Star, to)
	prefix := fmt.Sprintf("%s may not depend on %s: ", starName(from), starName(to))
	if to.Star == branch {
		return prefix + fmt.Sprintf("an assembler reaches a child branch only through its core, and %s is a spoke of branch %s; let %s build it, and use that", to.Spoke, branch, this.builderOf(branch))
	}

	return prefix + fmt.Sprintf("an assembler reaches only the cores of its child branches, one star in; let the core of branch %s stand for it", branch)
}

// starGuidance describes, for people, what a module placed in the star
// tree may do.
func (this Architecture) starGuidance(placement Placement) string {
	dir, parent := placement.Star, this.Parent(placement.Star)
	where := "star " + dir
	if parent != "" {
		where += ", a branch of " + parent
	}

	around := ""
	if ancestors := this.ancestors(dir); len(ancestors) > 0 {
		around = fmt.Sprintf(" and of the %s around it, %s", plural(len(ancestors), "star", "stars"), joinAnd(ancestors))
	}

	branches := this.branchesOf(dir)
	switch placement.Layer {
	case graph.LayerContracts:
		return fmt.Sprintf("Contracts %s of %s: interfaces, value types and their pure methods, errors and constants, importing only the standard library and declaring no package-level variable but sentinel errors. They may use the contracts of star %s%s, never a spoke, an engine or an assembler.", placement.Entry, where, dir, around)
	case graph.LayerEngine:
		return fmt.Sprintf("Engine %s of %s: runs policy over contracts. It may use the contracts of star %s%s and the engines of star %s, never a spoke or an assembler.", placement.Entry, where, dir, around, dir)
	case graph.LayerAssembler:
		parts := []string{"the core and the spokes of star " + dir}
		if len(branches) > 0 {
			parts = append(parts, fmt.Sprintf("the %s of its %s %s", plural(len(branches), "core", "cores"), plural(len(branches), "branch", "branches"), joinAnd(branches)))
		}

		if ancestors := this.ancestors(dir); len(ancestors) > 0 {
			parts = append(parts, fmt.Sprintf("the contracts of the %s around it, %s", plural(len(ancestors), "star", "stars"), joinAnd(ancestors)))
		}

		ending := "nothing else"
		if len(branches) > 0 {
			ending = "never a branch's spokes"
		}

		return fmt.Sprintf("Assembler %s of %s: wires its star. It may use %s, %s; no spoke may use it.", placement.Entry, where, joinAnd(parts), ending)
	case graph.LayerLeaf:
		star := this.star(dir)
		if (star == nil || len(star.Contracts)+len(star.Engines) == 0) && parent == "" {
			return fmt.Sprintf("Leaf %s of %s: may use only itself, since star %s has no contracts or engines yet; list them under stars in treaty.yaml.", placement.Spoke, where, dir)
		}

		uses := "the contracts and engines of star " + dir
		if ancestors := this.ancestors(dir); len(ancestors) > 0 {
			uses += fmt.Sprintf(" and the contracts of the %s around it, %s", plural(len(ancestors), "star", "stars"), joinAnd(ancestors))
		}

		return fmt.Sprintf("Leaf %s of %s: may use itself, %s, never an assembler or a sibling spoke. A leaf declares the interfaces it consumes; to use a sibling, depend on a port in the contracts of star %s and let an assembler of star %s hand the sibling in.",
			placement.Spoke, where, uses, dir, dir)
	}

	switch {
	case placement.Entry != "":
		return fmt.Sprintf("Unclassified: it lies beneath %s of %s, and a core role covers only the package an entry names; list it under that star's contracts, assemblers or engines, or move it out of %s. Until then its dependencies are not checked.", starName(this.claimed(placement)), where, placement.Entry)
	case placement.Spoke != "":
		return fmt.Sprintf("Unclassified: %s holds a package where treaty.yaml's stars look through it, so its dependencies are not checked.", placement.Spoke)
	case placement.Star != "":
		return fmt.Sprintf("Unclassified: the own package of star %s holds code, so treaty.yaml must list it in that star's contracts, assemblers or engines; until then its dependencies are not checked.", placement.Star)
	}

	return "Unclassified: treaty.yaml places this module nowhere, so its dependencies are not checked."
}

// starMayUse lists what a module placed in the star tree may depend on, in
// this order: its own leaf; its star's contracts, and for an assembler its
// whole core and its leaves, or for a leaf or an engine its engines; for an
// assembler its branches' cores; and the contracts of the stars around it.
func (this Architecture) starMayUse(placement Placement) []string {
	star := this.star(placement.Star)
	if star == nil || placement.Layer == graph.LayerUnclassified {
		return []string{"every module, unchecked"}
	}

	var result []string
	core := func(star *Star, layers ...string) {
		for _, role := range starRoles {
			if slices.Contains(layers, role.layer) {
				for _, entry := range role.entries(*star) {
					result = append(result, role.layer+" "+entry)
				}
			}
		}
	}

	all := []string{graph.LayerContracts, graph.LayerAssembler, graph.LayerEngine}
	switch placement.Layer {
	case graph.LayerLeaf:
		result = append(result, starName(placement))
		core(star, graph.LayerContracts, graph.LayerEngine)
	case graph.LayerContracts:
		core(star, graph.LayerContracts)
	case graph.LayerEngine:
		core(star, graph.LayerContracts, graph.LayerEngine)
	case graph.LayerAssembler:
		core(star, all...)
		result = append(result, "the leaves of star "+star.Dir)
		for _, branch := range this.branchesOf(star.Dir) {
			core(this.star(branch), all...)
		}
	}

	for _, ancestor := range this.ancestors(star.Dir) {
		core(this.star(ancestor), graph.LayerContracts)
	}

	return result
}

// starSummary states the snowflake's rules and its stars in one paragraph.
func (this Architecture) starSummary() string {
	var stars []string
	for _, star := range this.ordered() {
		var parts []string
		if parent := this.Parent(star.Dir); parent != "" {
			parts = append(parts, "branch of "+parent)
		}

		for _, role := range starRoles {
			if entries := role.entries(star); len(entries) > 0 {
				parts = append(parts, role.list+" "+joinAnd(entries))
			} else {
				parts = append(parts, "no "+role.list)
			}
		}

		stars = append(stars, fmt.Sprintf("%s (%s)", star.Dir, strings.Join(parts, "; ")))
	}

	return "Rules (snowflake): the repository is a star of stars. Each star has a core, holding its contracts, its assemblers and its engines, and spokes, its other children that hold a package, found by looking through directories that hold none: a spoke is a leaf, or a branch, a star of its own. A core role covers exactly the package an entry names; a package beneath one that no entry names is unclassified. " +
		"Contracts use only contracts of their own star and its ancestors. An engine runs policy over contracts only: those contracts and its own star's engines. An assembler composes: its own star's core and spokes, each child branch's core and the contracts of its ancestors. A spoke may use its own leaf, its star's contracts and engines and the contracts of every ancestor; a spoke never uses an assembler. " +
		"Nothing else: no dependency runs sideways, not even between the cores of sibling branches. A leaf declares the interfaces it consumes; spokes use ancestor contracts directly; there are no re-exports. Stars: " + strings.Join(stars, ", ") + "."
}

// validateStars checks a snowflake architecture: no setting of another
// style, the root star ".", every star a clean directory listed once, and
// every entry a clean directory inside its own star, outside its branches,
// holding no star and no other entry, and listed once.
func (this Architecture) validateStars() error {
	if len(this.Composition) > 0 {
		return fmt.Errorf("%w: composition does not apply to the snowflake architecture: assemblers do its work, so list main packages, factories and the public API among the assemblers of their star", ErrArchitecture)
	}

	if len(this.Layers) > 0 || len(this.Shared) > 0 || len(this.Boundaries) > 0 || len(this.Public) > 0 {
		return fmt.Errorf("%w: layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share", ErrArchitecture)
	}

	if len(this.Stars) == 0 {
		return fmt.Errorf("%w: the snowflake architecture needs its stars: declare the repository, \".\", with its contracts, assemblers and engines, such as .: {contracts: [\"internal/contracts\"], assemblers: [\".\"]}", ErrArchitecture)
	}

	seen := map[string]bool{}
	for _, star := range this.Stars {
		dir := star.Dir
		if dir == "" || strings.ContainsAny(dir, wildcards) || slices.Contains(strings.Split(dir, "/"), "..") || strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "./") || (dir != "." && strings.HasSuffix(dir, "/")) {
			return fmt.Errorf("%w: star %q must be a directory path relative to the repository root, not a glob", ErrArchitecture, dir)
		}

		if seen[dir] {
			return fmt.Errorf("%w: star %s is listed twice", ErrArchitecture, dir)
		}

		seen[dir] = true
	}

	if !seen["."] {
		var outermost []string
		for _, star := range this.ordered() {
			if this.Parent(star.Dir) == "" {
				outermost = append(outermost, star.Dir)
			}
		}

		return fmt.Errorf("%w: the snowflake's root star is the repository itself: declare \".\" under stars, around %s", ErrArchitecture, joinAnd(outermost))
	}

	for _, star := range this.ordered() {
		for _, role := range starRoles {
			for i, entry := range role.entries(star) {
				if err := this.validateEntry(star, role, entry); err != nil {
					return err
				}

				if slices.Contains(role.entries(star)[:i], entry) {
					return fmt.Errorf("%w: %s entry %q of star %s is listed twice", ErrArchitecture, role.list, entry, star.Dir)
				}

				if other := star.role(entry); other != nil && other.list != role.list {
					return fmt.Errorf("%w: %q is listed under both the %s and the %s of star %s; a core package has one role", ErrArchitecture, entry, other.list, role.list, star.Dir)
				}
			}
		}
	}

	return nil
}

// wildcards are the glob characters a star or an entry may not hold. They
// cannot be in a directory's name on Windows, so a path holding one is a
// glob; brackets can, as in app/[slug], so they are read literally.
const wildcards = "*?"

// validateEntry checks one entry of a star: a clean directory, not a glob,
// inside the star and outside its branches, holding no star. An entry may
// lie beneath another, since each names exactly one package.
func (this Architecture) validateEntry(star Star, role starRole, entry string) error {
	if entry == star.Dir {
		return nil
	}

	switch {
	case !clean(entry):
		return fmt.Errorf("%w: %s entry %q of star %s must be a directory path relative to the repository root, with no empty, . or .. segment", ErrArchitecture, role.list, entry, star.Dir)
	case strings.ContainsAny(entry, wildcards):
		return fmt.Errorf("%w: %s entry %q of star %s is a glob; name each package by its directory, so a new directory stays a spoke until treaty.yaml names it", ErrArchitecture, role.list, entry, star.Dir)
	case !encloses(star.Dir, entry):
		return fmt.Errorf("%w: %s entry %q of star %s lies outside %s; a star's core is inside its directory", ErrArchitecture, role.list, entry, star.Dir, star.Dir)
	}

	for _, other := range this.ordered() {
		if other.Dir != star.Dir && encloses(star.Dir, other.Dir) && encloses(other.Dir, entry) {
			return fmt.Errorf("%w: %s entry %q of star %s lies inside branch %s, whose core is its own; list it under %s", ErrArchitecture, role.list, entry, star.Dir, other.Dir, other.Dir)
		}
	}

	for _, other := range this.ordered() {
		if other.Dir != star.Dir && encloses(entry, other.Dir) {
			return fmt.Errorf("%w: %s entry %q of star %s holds star %s; a core package is one package and those nested in it, never a star", ErrArchitecture, role.list, entry, star.Dir, other.Dir)
		}
	}

	return nil
}

// binding is the architecture's binding, or for an unbound one a binding
// to a single module path, so every directory but the stars, the entries
// and that path is looked through.
func (this Architecture) binding(modulePath string) *starBinding {
	if this.bound != nil {
		return this.bound
	}

	var paths []string
	if modulePath != "" {
		paths = []string{modulePath}
	}

	bound, _ := this.Bind(paths)
	return bound.bound
}

// position finds the shallowest opaque directory on the way down from a
// star, exclusive, to a path inside it, inclusive: an entry, a star, or a
// directory holding a module that is not a misfit. The path itself counts
// as opaque.
func (this Architecture) position(binding *starBinding, star, modulePath string) string {
	for _, dir := range descend(star, modulePath) {
		if dir == modulePath || this.star(dir) != nil || this.entry(dir) || (binding.holds[dir] && !binding.misfits[dir]) {
			return dir
		}
	}

	return modulePath
}

// entry reports whether a directory is an entry of some star, other than
// a star's own directory.
func (this Architecture) entry(dir string) bool {
	for _, star := range this.Stars {
		if star.Dir != dir && star.role(dir) != nil {
			return true
		}
	}

	return false
}

// firstHolding finds the outermost directory strictly between two
// directories that holds a module, or nothing.
func (this *starBinding) firstHolding(outer, inner string) string {
	for _, dir := range descend(outer, inner) {
		if dir != inner && this.holds[dir] {
			return dir
		}
	}

	return ""
}

// firstSpoke finds the outermost directory strictly between a star and one
// of its entries that holds a module and is no entry itself: the spoke the
// entry would lie in. An entry beneath another entry lies in no spoke.
func (this *starBinding) firstSpoke(architecture Architecture, star, entry string) string {
	for _, dir := range descend(star, entry) {
		if dir != entry && this.holds[dir] && !architecture.entry(dir) {
			return dir
		}
	}

	return ""
}

// below lists the shallowest modules strictly inside a directory.
func (this *starBinding) below(dir string) (result []string) {
	for _, modulePath := range this.modules {
		if modulePath != dir && encloses(dir, modulePath) && !slices.ContainsFunc(result, func(outer string) bool { return encloses(outer, modulePath) }) {
			result = append(result, modulePath)
		}
	}

	return result
}

// misfit records a directory looked through although it holds a module.
func (this *starBinding) misfit(dir, short, long string) {
	this.misfits[dir] = true
	this.problems = append(this.problems, long)
	this.shorts = append(this.shorts, short)
}

// ancestors lists the stars around a star, its parent first.
func (this Architecture) ancestors(dir string) (result []string) {
	for parent := this.Parent(dir); parent != ""; parent = this.Parent(parent) {
		result = append(result, parent)
	}

	return result
}

// between lists the stars strictly between an outer star and an inner one
// inside it, outermost first.
func (this Architecture) between(outer, inner string) (result []string) {
	if !above(outer, inner) {
		return nil
	}

	for dir := this.Parent(inner); dir != "" && dir != outer; dir = this.Parent(dir) {
		result = append([]string{dir}, result...)
	}

	return result
}

// branchesOf lists the stars whose parent is a star, by directory.
func (this Architecture) branchesOf(dir string) []string {
	result := []string{}
	for _, star := range this.Stars {
		if star.Dir != dir && this.Parent(star.Dir) == dir {
			result = append(result, star.Dir)
		}
	}

	sort.Strings(result)
	return result
}

// builderOf names an assembler of a star for rule text: the star's own, or
// a new one when it lists none, which ends in a comma since it closes an
// aside.
func (this Architecture) builderOf(dir string) string {
	if star := this.star(dir); star != nil && len(star.Assemblers) > 0 {
		return fmt.Sprintf("an assembler of star %s (%s)", dir, joinAnd(star.Assemblers))
	}

	return fmt.Sprintf("a new assembler of star %s, such as %s,", dir, path.Join(dir, "assembly"))
}

// childOf names the child of a star that holds a placement in the star
// tree below it: its entry or leaf when the placement lies in that star,
// or else the branch of that star around the placement's star.
func (this Architecture) childOf(dir string, placement Placement) string {
	if placement.Star == dir {
		if placement.Spoke != "" {
			return placement.Spoke
		}

		return placement.Entry
	}

	result := placement.Star
	for result != "" && this.Parent(result) != dir {
		result = this.Parent(result)
	}

	return result
}

// common finds the deepest star around two stars.
func (this Architecture) common(a, b string) string {
	for a != "" && !encloses(a, b) {
		a = this.Parent(a)
	}

	return a
}

// contractsOf names a star's contracts for rule text: the ones it lists,
// or new ones when it lists none.
func (this Architecture) contractsOf(dir string) string {
	if star := this.star(dir); star != nil && len(star.Contracts) > 0 {
		return fmt.Sprintf("the contracts of star %s (%s)", dir, joinAnd(star.Contracts))
	}

	return fmt.Sprintf("new contracts of star %s, such as %s", dir, path.Join(dir, "contracts"))
}

// innermost finds the deepest star whose directory holds a module path, or
// nil when no star does.
func (this Architecture) innermost(modulePath string) (result *Star) {
	for i, star := range this.Stars {
		if encloses(star.Dir, modulePath) && (result == nil || depth(star.Dir) > depth(result.Dir)) {
			result = &this.Stars[i]
		}
	}

	return result
}

// ordered lists the stars the root first and each star before the stars
// inside it, siblings by directory.
func (this Architecture) ordered() []Star {
	result := slices.Clone(this.Stars)
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i].Dir, result[j].Dir
		if a == "." || b == "." {
			return a == "." && b != "."
		}

		return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/")) < 0
	})

	return result
}

// star finds a star by its directory.
func (this Architecture) star(dir string) *Star {
	for i, star := range this.Stars {
		if star.Dir == dir {
			return &this.Stars[i]
		}
	}

	return nil
}

// above reports whether a star lies strictly around another.
func above(outer, inner string) bool { return outer != inner && encloses(outer, inner) }

// clean reports whether a path relative to the repository root is written
// the one way a module path is: no leading or trailing slash, and no empty,
// . or .. segment.
func clean(entry string) bool {
	for _, segment := range strings.Split(entry, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}

	return true
}

// depth counts a directory's segments; the repository root has none.
func depth(dir string) int {
	if dir == "." {
		return 0
	}

	return strings.Count(dir, "/") + 1
}

// descend lists the directories on the way down from a directory,
// exclusive, to a path inside it, inclusive.
func descend(outer, inner string) (result []string) {
	if !encloses(outer, inner) || outer == inner {
		return nil
	}

	at := outer
	for _, segment := range strings.Split(relative(outer, inner), "/") {
		at = path.Join(at, segment)
		result = append(result, at)
	}

	return result
}

// encloses reports whether a directory holds a path, or is that path. The
// repository root holds everything.
func encloses(dir, inner string) bool {
	return dir == "." || inner == dir || strings.HasPrefix(inner, dir+"/")
}

// isCore reports whether a placement is in a star's core region.
func isCore(placement Placement) bool {
	return placement.Layer == graph.LayerContracts || placement.Layer == graph.LayerAssembler || placement.Layer == graph.LayerEngine
}

// joinAnd lists words as prose: a, b and c.
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// listOrEmpty copies a list, empty rather than nil.
func listOrEmpty(list []string) []string {
	return append([]string{}, list...)
}

// plural picks a word's form for a count.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}

	return many
}

// relative is a path below a directory, relative to it.
func relative(dir, inner string) string {
	if dir == "." {
		return inner
	}

	return strings.TrimPrefix(inner, dir+"/")
}

// starName describes a snowflake placement for rule text: contracts E,
// assembler E, engine E or leaf L.
func starName(placement Placement) string {
	switch placement.Layer {
	case graph.LayerContracts, graph.LayerAssembler, graph.LayerEngine:
		return placement.Layer + " " + placement.Entry
	case graph.LayerLeaf:
		return "leaf " + placement.Spoke
	default:
		return placement.Layer
	}
}
