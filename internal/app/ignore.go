package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrNotIgnorable = errors.New("that finding cannot be ignored")

// Ignore is one finding treaty.yaml ignores, under rules.ignore: its kind
// and target, which identify it across edits, the fingerprint of what it
// said when it was ignored, and why. Line is where the entry starts in the
// file, or 0.
type Ignore struct {
	Finding     string `json:"finding"`
	Target      string `json:"target"`
	Fingerprint string `json:"fingerprint"`
	Reason      string `json:"reason"`
	Line        int    `json:"line,omitempty"`
}

// Ignorable reports whether a kind of finding can be ignored: every kind but
// the implementation-only changes, which gather many symbols and have no
// target of their own.
func Ignorable(kind string) bool {
	return kind != rules.FindingImplementation
}

// applyIgnores marks each finding treaty.yaml ignores. One whose
// fingerprint still matches is ignored, so it neither fails nor warns. One
// whose fingerprint changed counts again, marked as once ignored, until it
// is ignored anew.
//
// Parameters:
//   - findings: the ranked findings.
//   - ignores: the entries of rules.ignore.
//   - head: the graph the findings are about.
//
// Returns:
//   - result: the findings, marked.
//   - stale: the entries whose target is no longer in the code at all.
func applyIgnores(findings []rules.Finding, ignores []Ignore, head *graph.Graph) (result []rules.Finding, stale []Ignore) {
	byKey := map[[2]string]Ignore{}
	for _, ignore := range ignores {
		byKey[[2]string{ignore.Finding, ignore.Target}] = ignore
	}

	matched := map[[2]string]bool{}
	result = make([]rules.Finding, len(findings))
	for i, finding := range findings {
		key := [2]string{finding.Kind, finding.Target}
		if ignore, ok := byKey[key]; ok && finding.Target != "" {
			matched[key] = true
			if ignore.Fingerprint == finding.Fingerprint {
				finding.Ignored = ignore.Reason
			} else {
				finding.WasIgnored = ignore.Reason
			}
		}

		result[i] = finding
	}

	// An entry no finding matches is only stale once its target is gone: a
	// breaking change ignored in one pull request is no finding after it
	// merges, yet its symbol is still there.
	for _, ignore := range ignores {
		if !matched[[2]string{ignore.Finding, ignore.Target}] && !targetExists(head, ignore.Target) {
			stale = append(stale, ignore)
		}
	}

	return result, stale
}

// targetExists reports whether a finding's target is in the graph: a
// module edge, a module or a symbol.
func targetExists(head *graph.Graph, target string) bool {
	if from, to, edge := strings.Cut(target, " -> "); edge {
		for _, moduleEdge := range head.ModuleEdges() {
			if moduleEdge.From == from && moduleEdge.To == to {
				return true
			}
		}

		return false
	}

	return head.Module(target) != nil || head.Symbol(target) != nil
}

// staleNotes asks for each stale entry to be removed.
func staleNotes(stale []Ignore) (result []string) {
	for _, ignore := range stale {
		where := ""
		if ignore.Line > 0 {
			where = fmt.Sprintf(" at line %d", ignore.Line)
		}

		result = append(result, fmt.Sprintf("treaty.yaml ignores %s %s%s, which is no longer in the code; remove the entry", ignore.Finding, ignore.Target, where))
	}

	return result
}

// Ignore ignores a finding of the current analysis in treaty.yaml, with
// its fingerprint as it stands, and rebuilds, so the map, the checks and
// agents follow. Ignoring a finding already ignored, such as one that
// changed since, takes its new fingerprint and reason.
//
// Parameters:
//   - kind: the finding's kind.
//   - target: the finding's target.
//   - reason: why it is ignored; required.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: there is no such finding, it cannot be ignored, there is no
//     reason, or treaty.yaml could not be written.
//
// Errors:
//   - ErrNotIgnorable: the kind cannot be ignored, there is no reason, or
//     no current finding has that kind and target.
//   - ErrNotReady: the graph has not been built yet.
func (this *Live) Ignore(kind, target, reason string) (path string, err error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("%w: say why it is ignored, so a reviewer can judge it", ErrNotIgnorable)
	}

	if !Ignorable(kind) || target == "" {
		return "", fmt.Errorf("%w: %s findings gather many symbols", ErrNotIgnorable, kind)
	}

	current, err := this.current()
	if err != nil {
		return "", err
	}

	for _, finding := range current.findings {
		if finding.Kind == kind && finding.Target == target {
			if path, err = this.service.config.Ignore(Ignore{Finding: kind, Target: target, Fingerprint: finding.Fingerprint, Reason: reason}); err != nil {
				return "", err
			}

			return path, this.Refresh()
		}
	}

	return "", fmt.Errorf("%w: there is no %s finding for %s", ErrNotIgnorable, kind, target)
}

// Unignore stops ignoring a finding: it removes its entry from treaty.yaml
// and rebuilds.
//
// Parameters:
//   - kind: the finding's kind.
//   - target: the finding's target.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: treaty.yaml could not be written.
func (this *Live) Unignore(kind, target string) (path string, err error) {
	if path, err = this.service.config.Unignore(kind, target); err != nil {
		return "", err
	}

	return path, this.Refresh()
}
