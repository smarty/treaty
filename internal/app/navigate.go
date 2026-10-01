package app

// Allowed says whether one module may depend on another, from the working
// tree, before the import is written.
//
// Parameters:
//   - from: the depending module, as an id such as go:internal/app, a path,
//     or a short name such as app; a symbol or file stands for its module.
//     A path that matches nothing is a module not built yet.
//   - to: the module depended on, given the same ways.
//
// Returns:
//   - result: the verdict and the rule behind it, one line each.
//   - err: the tree or config could not be read, or a name is ambiguous.
//
// Errors:
//   - ErrAmbiguousTarget: a short name matches more than one thing.
func (this *Service) Allowed(from, to string) (result string, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return "", err
	}

	return analysis.allowed(from, to)
}

// Find lists the working tree's symbols whose id contains the query,
// ignoring case.
//
// Parameters:
//   - query: the text to look for.
//   - kind: function, method, interface, type or value; empty for any.
//
// Returns:
//   - result: one line per symbol, id, kind, location and signature, contracts
//     first.
//   - err: the tree or config could not be read.
func (this *Service) Find(query, kind string) (result string, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return "", err
	}

	return analysis.find(query, kind), nil
}

// Impact lists everything in the working tree that depends on a symbol or
// module, transitively: what an edit to it can break.
//
// Parameters:
//   - target: a symbol id, module id or file, or a short name that matches
//     exactly one, such as Store.CreateBook or store.go.
//
// Returns:
//   - result: the dependents grouped by module, contracts first.
//   - err: the tree or config could not be read, or the target does not exist.
//
// Errors:
//   - ErrUnknownTarget: nothing matches the target.
//   - ErrAmbiguousTarget: several symbols, modules or files match it.
func (this *Service) Impact(target string) (result string, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return "", err
	}

	return analysis.impact(target)
}

// Overview describes the working tree's architecture compactly: every module
// by layer with its contract count, and every module dependency.
//
// Parameters:
//   - base: a git ref to count contract changes against, or empty for none.
//
// Returns:
//   - result: the overview text.
//   - err: the tree, config or base could not be read.
func (this *Service) Overview(base string) (result string, err error) {
	analysis, err := this.analyze(base)
	if err != nil {
		return "", err
	}

	return analysis.overview(), nil
}
