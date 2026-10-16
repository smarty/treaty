package rules

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

// Star is one star of the snowflake architecture: a directory relative to
// the repository root, such as internal or internal/blocks, and the globs
// of its core. Its own package is in its core unless a composition entry
// names it exactly, so Core may be empty. Every other child directory of
// the star that holds modules is a spoke: a leaf, or a branch when it is a
// star of its own.
//
// Notes:
//   - A spoke may use its own star's core and the cores of every star
//     around it, never a sibling spoke or anything in a sibling branch, not
//     even that branch's core.
//   - A core may use itself and the cores of the stars around it, never its
//     own spokes.
type Star struct {
	Dir  string
	Core []string
}

// StarNode is one star in the star tree, for people and the map: its
// directory, the star around it, how deep it sits, the globs of its core,
// and its spokes. Leaves are the spoke directories that hold at least one
// module, and Branches the child directories that are stars of their own.
// The root star has no parent and depth 0.
type StarNode struct {
	Dir      string   `json:"dir"`
	Parent   string   `json:"parent,omitempty"`
	Depth    int      `json:"depth"`
	Core     []string `json:"core"`
	Leaves   []string `json:"leaves"`
	Branches []string `json:"branches"`
}

// Tree lists every star of a snowflake architecture with its leaves and
// branches, the root first and each star before the stars inside it.
//
// Parameters:
//   - modulePaths: the paths of the modules to find the leaves among.
//
// Returns:
//   - result: one node per star; empty for any other style.
func (this Architecture) Tree(modulePaths []string) []StarNode {
	if this.Style != StyleSnowflake {
		return nil
	}

	leaves := map[string][]string{}
	for _, modulePath := range modulePaths {
		placement := this.Resolve(modulePath)
		if placement.Layer == graph.LayerSpoke && !slices.Contains(leaves[placement.Star], placement.Spoke) {
			leaves[placement.Star] = append(leaves[placement.Star], placement.Spoke)
		}
	}

	stars := this.ordered()
	result := make([]StarNode, 0, len(stars))
	for _, star := range stars {
		node := StarNode{Dir: star.Dir, Depth: len(this.around(star.Dir)) - 1, Core: slices.Clone(star.Core), Leaves: leaves[star.Dir], Branches: []string{}}
		if node.Depth > 0 {
			node.Parent = path.Dir(star.Dir)
		}

		if node.Core == nil {
			node.Core = []string{}
		}

		if node.Leaves == nil {
			node.Leaves = []string{}
		}

		sort.Strings(node.Leaves)
		for _, other := range stars {
			if other.Dir != star.Dir && path.Dir(other.Dir) == star.Dir {
				node.Branches = append(node.Branches, other.Dir)
			}
		}

		result = append(result, node)
	}

	return result
}

// Unmatched lists, for people, each star of a snowflake architecture that
// holds no module, and each core glob that matches none. Neither stops the
// architecture from loading, but a misspelled branch demotes the real one
// to a leaf of its parent, whose packages may then use each other.
//
// Parameters:
//   - modulePaths: the paths of the modules to look for.
//
// Returns:
//   - result: one note per empty star or core glob; empty for any other
//     style.
func (this Architecture) Unmatched(modulePaths []string) (result []string) {
	if this.Style != StyleSnowflake {
		return nil
	}

	for _, star := range this.ordered() {
		if !slices.ContainsFunc(modulePaths, func(modulePath string) bool { return encloses(star.Dir, modulePath) }) {
			result = append(result, fmt.Sprintf("treaty.yaml declares star %s under stars, but no module lies in it; check the directory's spelling", star.Dir))
			continue
		}

		for _, entry := range star.Core {
			if !slices.ContainsFunc(modulePaths, func(modulePath string) bool { return Match(entry, modulePath) }) {
				result = append(result, fmt.Sprintf("core glob %q of star %s matches no module; check its spelling", entry, star.Dir))
			}
		}
	}

	return result
}

// Crossing names, for a dependency the snowflake refuses, the star whose
// children it crosses between and the child directory of that star that
// holds the module depended on: a spoke of it, or a branch around it. A
// port for the dependency belongs in that star's core, and composition
// hands that child in.
//
// Parameters:
//   - from: the depending module's placement, a core or a spoke.
//   - to: the placement of the module depended on, a core or a spoke.
//
// Returns:
//   - star: the deepest star around both.
//   - child: the child directory of star that holds to.
func Crossing(from, to Placement) (star, child string) {
	star = from.Star
	for !encloses(star, to.Star) {
		star = path.Dir(star)
	}

	return star, childOf(star, to)
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
// directory holds a module path, whatever composition says about it.
//
// Parameters:
//   - modulePath: the module path relative to the repository root.
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

// resolveSnowflake places one module path in the star tree. The first step
// that matches places it:
//
//  1. a composition entry that is exactly the path;
//  2. a core entry of some star that is exactly the path;
//  3. a star's own package is in its core, so no composition glob, such
//     as one ending in /**, takes a star's hub from it;
//  4. a composition glob;
//  5. outside every star, the module is unclassified;
//  6. a core glob of the innermost star, and only of that star, so an
//     outer core never reaches into a branch;
//  7. otherwise the module is in the spoke named by the child directory of
//     the innermost star that holds it, nested packages included.
func (this Architecture) resolveSnowflake(modulePath string) Placement {
	if slices.Contains(this.Composition, modulePath) {
		return Placement{Layer: graph.LayerComposition}
	}

	for _, star := range this.Stars {
		if slices.Contains(star.Core, modulePath) {
			return Placement{Layer: graph.LayerCore, Star: star.Dir}
		}
	}

	if this.star(modulePath) != nil {
		return Placement{Layer: graph.LayerCore, Star: modulePath}
	}

	if MatchAny(this.Composition, modulePath) {
		return Placement{Layer: graph.LayerComposition}
	}

	star := this.innermost(modulePath)
	switch {
	case star == nil:
		return Placement{Layer: graph.LayerUnclassified}
	case MatchAny(star.Core, modulePath):
		return Placement{Layer: graph.LayerCore, Star: star.Dir}
	}

	child, _, _ := strings.Cut(relative(star.Dir, modulePath), "/")
	return Placement{Layer: graph.LayerSpoke, Star: star.Dir, Spoke: path.Join(star.Dir, child)}
}

// checkSnowflake applies the snowflake's rules between two placed modules,
// neither of them composition or unclassified. A dependency is allowed
// exactly when it points at the core of a star around the depending
// module's star, or stays inside one leaf.
func (this Architecture) checkSnowflake(from, to Placement) (bool, string) {
	if to.Layer == graph.LayerCore && encloses(to.Star, from.Star) {
		return true, ""
	}

	if from.Layer == graph.LayerSpoke && to.Layer == graph.LayerSpoke && from.Spoke == to.Spoke {
		return true, ""
	}

	common, theirs := Crossing(from, to)
	if from.Layer == graph.LayerCore && from.Star == common {
		return false, fmt.Sprintf("%s may not depend on %s: %s is a spoke of star %s, and a core may use only itself and the cores of the stars around it; declare a port in the core of star %s, and let composition hand %s in",
			starName(from), starName(to), theirs, common, common, theirs)
	}

	through := ""
	if to.Layer == graph.LayerCore {
		through = ", not even through a branch's core"
	}

	return false, fmt.Sprintf("%s may not depend on %s: %s and %s are sibling spokes of star %s, and spokes never depend sideways%s; declare a port in the core of star %s, implement it in %s, and let composition hand it in",
		starName(from), starName(to), childOf(common, from), theirs, common, through, common, theirs)
}

// starGuidance describes, for people, what a module placed in the star
// tree may do.
func (this Architecture) starGuidance(placement Placement) string {
	around := this.around(placement.Star)
	switch placement.Layer {
	case graph.LayerCore:
		if len(around) == 1 {
			return fmt.Sprintf("Core of star %s: its contracts and orchestrators. It may use only itself, never its own spokes; reach a spoke through a port declared here, which composition hands in.", placement.Star)
		}

		return fmt.Sprintf("Core of star %s, a branch of %s: its contracts and orchestrators. It may use itself and %s, never its own spokes; reach a spoke through a port declared here, which composition hands in.",
			placement.Star, around[1], cores(around[1:]))
	case graph.LayerSpoke:
		return fmt.Sprintf("Spoke %s of star %s: may use itself and %s, never a sibling spoke or branch. To use a sibling, depend on a port in a core and let composition hand the sibling in.",
			placement.Spoke, placement.Star, cores(around))
	default:
		root := this.root()
		return fmt.Sprintf("Unclassified: outside the root star %s, so its dependencies are not checked. Add it to composition, or move it inside %s.", root, root)
	}
}

// starMayUse lists what a module placed in the star tree may depend on,
// innermost first.
func (this Architecture) starMayUse(placement Placement) []string {
	var result []string
	switch placement.Layer {
	case graph.LayerComposition:
		return []string{"every core and spoke", graph.LayerComposition}
	case graph.LayerUnclassified:
		return []string{"every core and spoke"}
	case graph.LayerSpoke:
		result = append(result, starName(placement))
	}

	for _, dir := range this.around(placement.Star) {
		result = append(result, fmt.Sprintf("core of %s (%s)", dir, strings.Join(append([]string{dir}, this.star(dir).Core...), ", ")))
	}

	return result
}

// starSummary states the snowflake's rules and its stars in one paragraph.
func (this Architecture) starSummary() string {
	var stars []string
	for _, star := range this.ordered() {
		core := "core its own package only"
		if len(star.Core) > 0 {
			core = "core " + joinAnd(star.Core)
		}

		if around := this.around(star.Dir); len(around) > 1 {
			core = fmt.Sprintf("branch of %s, %s", around[1], core)
		}

		stars = append(stars, fmt.Sprintf("%s (%s)", star.Dir, core))
	}

	return "Rules (snowflake): the code is a star of stars. Each star has one core, holding its contracts and orchestrators, and spokes, its other child directories; a spoke that is a star of its own is a branch. " +
		"A spoke may use itself, its own star's core and the cores of every star around it. A core may use itself and the cores around it, never its own spokes. " +
		"No spoke may use a sibling spoke or anything in a sibling branch, not even its core: an orchestrator reaches its spokes through ports declared in its core, and composition hands the implementations in. " +
		"Composition may use everything; nothing may use composition. Stars: " + strings.Join(stars, ", ") + "."
}

// validateStars checks a snowflake architecture: one root star, every other
// star a child directory of a star, and every core entry a clean path
// inside its own star, naming a child directory of it and outside its
// branches.
func (this Architecture) validateStars() error {
	if len(this.Layers) > 0 || len(this.Shared) > 0 || len(this.Boundaries) > 0 || len(this.Public) > 0 {
		return fmt.Errorf("%w: layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share", ErrArchitecture)
	}

	if len(this.Stars) == 0 {
		return fmt.Errorf("%w: the snowflake architecture needs at least one star; name its directory and its core under stars, such as internal: [\"internal/core/**\"]", ErrArchitecture)
	}

	seen := map[string]bool{}
	for _, star := range this.Stars {
		dir := star.Dir
		if dir == "" || strings.ContainsAny(dir, "*?[") || slices.Contains(strings.Split(dir, "/"), "..") || strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "./") || (dir != "." && strings.HasSuffix(dir, "/")) {
			return fmt.Errorf("%w: star %q must be a directory path relative to the repository root, not a glob", ErrArchitecture, dir)
		}

		if seen[dir] {
			return fmt.Errorf("%w: star %s is listed twice", ErrArchitecture, dir)
		}

		seen[dir] = true
	}

	var roots []string
	for _, star := range this.Stars {
		if this.outermost(star.Dir) {
			roots = append(roots, star.Dir)
		}
	}

	if len(roots) > 1 {
		return fmt.Errorf("%w: stars %s and %s are both outermost; a snowflake has one root star, so declare a star around both, such as \".\", or nest one inside the other", ErrArchitecture, roots[0], roots[1])
	}

	for _, star := range this.Stars {
		if star.Dir != roots[0] && this.star(path.Dir(star.Dir)) == nil {
			return fmt.Errorf("%w: star %s is not a spoke of a star: a branch must be a child directory of the star around it; declare %s as a star, or move the star", ErrArchitecture, star.Dir, path.Dir(star.Dir))
		}
	}

	for _, star := range this.Stars {
		for _, entry := range star.Core {
			if entry != star.Dir && !clean(entry) {
				return fmt.Errorf("%w: core glob %q of star %s must be a path relative to the repository root, with no empty, . or .. segment", ErrArchitecture, entry, star.Dir)
			}

			if !encloses(star.Dir, entry) {
				return fmt.Errorf("%w: core glob %q of star %s lies outside %s; a star's core is inside its directory", ErrArchitecture, entry, star.Dir, star.Dir)
			}

			if child, _, _ := strings.Cut(relative(star.Dir, entry), "/"); entry != star.Dir && strings.ContainsAny(child, "*?[") {
				example := path.Join(star.Dir, "core") + "/**"
				return fmt.Errorf("%w: core glob %q of star %s does not name a directory of its own; start it with a child directory of %s, such as %s", ErrArchitecture, entry, star.Dir, star.Dir, example)
			}

			for _, branch := range this.Stars {
				if branch.Dir != star.Dir && path.Dir(branch.Dir) == star.Dir && encloses(branch.Dir, entry) {
					return fmt.Errorf("%w: core glob %q of star %s lies inside branch %s, whose core is its own; list it under %s", ErrArchitecture, entry, star.Dir, branch.Dir, branch.Dir)
				}
			}
		}
	}

	return nil
}

// around lists a star and every star around it, innermost first.
func (this Architecture) around(dir string) (result []string) {
	for {
		if this.star(dir) != nil {
			result = append(result, dir)
		}

		if dir == "." {
			return result
		}

		dir = path.Dir(dir)
	}
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

// outermost reports whether no other star holds a star's directory.
func (this Architecture) outermost(dir string) bool {
	for _, other := range this.Stars {
		if other.Dir != dir && encloses(other.Dir, dir) {
			return false
		}
	}

	return true
}

// root is the directory of the star around every other.
func (this Architecture) root() string {
	for _, star := range this.Stars {
		if this.outermost(star.Dir) {
			return star.Dir
		}
	}

	return ""
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

// childOf names the child directory of star dir that holds a placement in
// the star tree below it: its spoke when the placement is in a spoke of
// dir, or else the branch of dir around the placement's star.
func childOf(dir string, placement Placement) string {
	if placement.Star == dir {
		return placement.Spoke
	}

	result := placement.Star
	for result != "." && path.Dir(result) != dir {
		result = path.Dir(result)
	}

	return result
}

// clean reports whether a path relative to the repository root is written
// the one way a module path is: no leading or trailing slash, and no empty,
// . or .. segment. A glob written any other way matches no module.
func clean(entry string) bool {
	for _, segment := range strings.Split(entry, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}

	return true
}

// cores names the cores of stars for guidance, such as the core of internal
// or the cores of internal/blocks and internal.
func cores(dirs []string) string {
	if len(dirs) == 1 {
		return "the core of " + dirs[0]
	}

	return "the cores of " + joinAnd(dirs)
}

// depth counts a directory's segments; the repository root has none.
func depth(dir string) int {
	if dir == "." {
		return 0
	}

	return strings.Count(dir, "/") + 1
}

// encloses reports whether a directory holds a path, or is that path. The
// repository root holds everything.
func encloses(dir, inner string) bool {
	return dir == "." || inner == dir || strings.HasPrefix(inner, dir+"/")
}

// joinAnd lists words as prose: a, b and c.
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}

	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// relative is a path below a directory, relative to it.
func relative(dir, inner string) string {
	if dir == "." {
		return inner
	}

	return strings.TrimPrefix(inner, dir+"/")
}

// starName describes a snowflake placement for rule text: core of star X,
// or spoke X.
func starName(placement Placement) string {
	switch placement.Layer {
	case graph.LayerCore:
		return "core of star " + placement.Star
	case graph.LayerSpoke:
		return "spoke " + placement.Spoke
	default:
		return placement.Layer
	}
}
