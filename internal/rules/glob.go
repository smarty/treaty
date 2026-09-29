// Package rules holds the mechanical checks: architecture rules,
// stability metrics, contract change classification and review ranking.
package rules

import (
	"path"
	"strings"
)

// Match reports whether a path matches a glob. A "**" segment matches zero
// or more path segments; other segments use path.Match rules.
//
// Parameters:
//   - glob: the pattern, such as internal/graph/**.
//   - value: the path to test.
//
// Returns:
//   - result: true on a match.
func Match(glob, value string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(value, "/"))
}

// MatchAny reports whether a path matches any of the globs.
//
// Parameters:
//   - globs: the patterns.
//   - value: the path to test.
//
// Returns:
//   - result: true when at least one glob matches.
func MatchAny(globs []string, value string) bool {
	for _, glob := range globs {
		if Match(glob, value) {
			return true
		}
	}

	return false
}

func matchSegments(glob, value []string) bool {
	if len(glob) == 0 {
		return len(value) == 0
	}

	if glob[0] == "**" {
		for i := 0; i <= len(value); i++ {
			if matchSegments(glob[1:], value[i:]) {
				return true
			}
		}

		return false
	}

	if len(value) == 0 {
		return false
	}

	ok, err := path.Match(glob[0], value[0])
	return err == nil && ok && matchSegments(glob[1:], value[1:])
}
