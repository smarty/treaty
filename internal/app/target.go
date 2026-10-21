package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/smarty/treaty/internal/graph"
)

const suggestionLimit = 8

var ErrAmbiguousTarget = errors.New("ambiguous target")

// resolve turns a target as a person or agent writes it into exactly one
// symbol id, module id or file path.
//
// Notes:
//   - An exact symbol id, module id or file path is used as given. A file
//     may carry its language prefix.
//   - Otherwise the target is a short name: a symbol's qualified name such
//     as Store.CreateBook, a method's own name such as CreateBook, or the
//     tail of an id or path after a slash or colon, such as app:Service,
//     storage or store.go. It resolves when exactly one symbol, module or
//     file matches.
//
// Parameters:
//   - target: the target text.
//
// Returns:
//   - result: the id or file path.
//   - err: nothing matches, or more than one thing does.
//
// Errors:
//   - ErrUnknownTarget: nothing matches; the message suggests near matches.
//   - ErrAmbiguousTarget: several things match; the message lists them.
func (this *analysis) resolve(target string) (result string, err error) {
	g := this.head
	if g.Symbol(target) != nil || g.Module(target) != nil {
		return target, nil
	}

	if module, file := fileTarget(g, target); module != nil {
		return file, nil
	}

	var matches []string
	for _, symbol := range g.Symbols {
		if symbol.Name == target || tailMatches(symbol.ID, target) || (symbol.Parent != "" && symbol.Name == symbol.Parent+"."+target) {
			matches = append(matches, symbol.ID)
			continue
		}

		// A name with a parameter list or generic markers, as in
		// Order.Add(Line,int) or Result<>.Get(int), also matches without them.
		if bare := bareName(symbol.Name); bare != symbol.Name {
			parent := bareName(symbol.Parent)
			if bare == target || tailMatches(symbol.Module+":"+bare, target) || (parent != "" && bare == parent+"."+target) {
				matches = append(matches, symbol.ID)
			}
		}
	}

	for _, module := range g.Modules {
		if module.Path == target || tailMatches(module.ID, target) {
			matches = append(matches, module.ID)
		}

		for _, file := range module.Files {
			if strings.HasSuffix(file, "/"+target) {
				matches = append(matches, file)
			}
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", unknown(g, target)
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("%w: %s matches %s; use one of them", ErrAmbiguousTarget, target, listed(matches))
	}
}

// listed joins at most suggestionLimit ids, counting the rest.
func listed(ids []string) string {
	if len(ids) <= suggestionLimit {
		return strings.Join(ids, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(ids[:suggestionLimit], ", "), len(ids)-suggestionLimit)
}

// bareName drops a name's parameter list, from the first (, and every <…>
// group, so Money.op_Implicit(int)Money is Money.op_Implicit and
// Result<>.Get(int) is Result.Get.
func bareName(name string) string {
	if cut := strings.Index(name, "("); cut >= 0 {
		name = name[:cut]
	}

	var builder strings.Builder
	depth := 0
	for _, r := range name {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

// tailMatches reports whether an id ends with the target after a slash or a
// colon, so app:Service matches go:internal/app:Service.
func tailMatches(id, target string) bool {
	return strings.HasSuffix(id, "/"+target) || strings.HasSuffix(id, ":"+target)
}

// unknown reports a target that matches nothing, suggesting the symbols,
// modules and files whose id contains it.
func unknown(g *graph.Graph, target string) error {
	needle := strings.ToLower(target)
	var near []string
	for _, symbol := range g.Symbols {
		if strings.Contains(strings.ToLower(symbol.ID), needle) {
			near = append(near, symbol.ID)
		}
	}

	for _, module := range g.Modules {
		if strings.Contains(strings.ToLower(module.ID), needle) {
			near = append(near, module.ID)
		}

		for _, file := range module.Files {
			if strings.Contains(strings.ToLower(file), needle) {
				near = append(near, file)
			}
		}
	}

	if len(near) == 0 {
		return fmt.Errorf("%w: %s; find lists symbols by name", ErrUnknownTarget, target)
	}

	sort.Strings(near)
	return fmt.Errorf("%w: %s; did you mean %s?", ErrUnknownTarget, target, listed(near))
}
