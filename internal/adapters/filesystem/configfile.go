package filesystem

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var (
	ErrConfigVersion = errors.New("unsupported treaty.yaml version")
	ErrUnknownKey    = errors.New("unknown key")
	ErrUnknownRule   = errors.New("unknown rule")
	ErrIgnore        = errors.New("invalid ignore entry")

	// ignoreKeys are the keys of each entry of rules.ignore, all required.
	ignoreKeys = []string{"finding", "target", "fingerprint", "reason"}

	// renamedRules maps rule names from before version 1 to their names
	// since, so the error for an old name can give the new one.
	renamedRules = map[string]string{"breaking": "breaking_changes", "cycle": "dependency_cycles"}

	// configKeys lists the keys of each mapping in treaty.yaml, by the
	// path of keys that leads to it.
	configKeys = map[string][]string{
		"":               {"version", "architecture", "composition", "layers", "shared", "boundaries", "public", "stars", "rules"},
		"rules":          {"fail_on", "warn_on", "ignore"},
		"layers.adapter": {graph.SideDriving, graph.SideDriven},
	}

	// starKeys are the keys of each star under stars, whose own keys are
	// directories.
	starKeys = []string{"contracts", "orchestrators"}
)

// decodeConfig reads treaty.yaml strictly. A version newer than this
// treaty's is refused before anything else, since its keys may be ones this
// treaty does not know. Then every key must be a known one and every rule
// one of app.RuleNames, so a typo cannot quietly switch a check off.
//
// Returns:
//   - result: the decoded file.
//   - versioned: false when the file has no version line.
//   - err: the file is not valid YAML, or not a treaty.yaml this treaty
//     reads.
//
// Errors:
//   - ErrConfigVersion: the version is not a whole number from 0 to
//     app.ConfigVersion.
//   - ErrUnknownKey: a key is misspelled or from a newer treaty.
//   - ErrUnknownRule: fail_on or warn_on names something other than a rule.
func decodeConfig(data []byte) (result configFile, versioned bool, err error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return result, false, err
	}

	if len(document.Content) == 0 {
		return result, false, nil
	}

	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return result, false, fmt.Errorf("line %d: treaty.yaml must map keys to values", root.Line)
	}

	if version := value(root, "version"); version != nil {
		number, err := strconv.Atoi(version.Value)
		if version.Kind != yaml.ScalarNode || version.Tag != "!!int" || err != nil || number < 0 {
			return result, true, fmt.Errorf("line %d: %w: version must be a whole number, such as %d", version.Line, ErrConfigVersion, app.ConfigVersion)
		}

		if number > app.ConfigVersion {
			return result, true, fmt.Errorf("line %d: %w: the file is version %d and this treaty reads up to version %d; upgrade treaty", version.Line, ErrConfigVersion, number, app.ConfigVersion)
		}

		versioned = true
	}

	if err := checkKeys(root, ""); err != nil {
		return result, versioned, err
	}

	if err := document.Decode(&result); err != nil {
		return result, versioned, err
	}

	if rulesNode := value(root, "rules"); rulesNode != nil {
		for _, key := range []string{"fail_on", "warn_on"} {
			if err := checkRules(value(rulesNode, key), key); err != nil {
				return result, versioned, err
			}
		}

		if err := checkIgnores(value(rulesNode, "ignore")); err != nil {
			return result, versioned, err
		}
	}

	return result, versioned, nil
}

// checkKeys refuses a key that the mapping at path does not have, and
// checks the mappings inside it that have known keys.
func checkKeys(mapping *yaml.Node, path string) error {
	known := configKeys[path]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, entry := mapping.Content[i], mapping.Content[i+1]
		if !slices.Contains(known, key.Value) {
			where := ""
			if path != "" {
				where = " under " + path
			}

			return fmt.Errorf("line %d: %w %q%s; %s", key.Line, ErrUnknownKey, key.Value, where, suggest(key.Value, known, "a newer treaty may know it"))
		}

		inner := strings.TrimPrefix(path+"."+key.Value, ".")
		if path == "" && key.Value == "stars" && entry.Kind == yaml.MappingNode {
			if err := checkStarKeys(entry); err != nil {
				return err
			}

			continue
		}

		if path == "" && key.Value == "layers" && entry.Kind == yaml.MappingNode {
			// Layer names are the config's own, so only the hexagonal
			// adapter, whose sides are fixed, has keys to check.
			if adapter := value(entry, graph.LayerAdapter); adapter != nil && adapter.Kind == yaml.MappingNode {
				if err := checkKeys(adapter, "layers.adapter"); err != nil {
					return err
				}
			}

			continue
		}

		if _, ok := configKeys[inner]; ok && entry.Kind == yaml.MappingNode {
			if err := checkKeys(entry, inner); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkStarKeys refuses a key under a star other than contracts and
// orchestrators. A star that is not a mapping is left for the
// architecture to refuse.
func checkStarKeys(stars *yaml.Node) error {
	for i := 0; i+1 < len(stars.Content); i += 2 {
		dir, star := stars.Content[i].Value, stars.Content[i+1]
		if star.Kind != yaml.MappingNode {
			continue
		}

		for j := 0; j+1 < len(star.Content); j += 2 {
			if key := star.Content[j]; !slices.Contains(starKeys, key.Value) {
				return fmt.Errorf("line %d: %w %q under stars.%s; %s", key.Line, ErrUnknownKey, key.Value, dir, suggest(key.Value, starKeys, "a star maps contracts and orchestrators"))
			}
		}
	}

	return nil
}

// checkRules refuses a rule list naming something other than a rule.
func checkRules(list *yaml.Node, key string) error {
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}

	names := make([]string, len(app.RuleNames))
	for i, rule := range app.RuleNames {
		names[i] = rule.Name
	}

	for _, item := range list.Content {
		if renamed, old := renamedRules[item.Value]; old {
			return fmt.Errorf("line %d: %s: %w %q; since version 1 it is %q", item.Line, key, ErrUnknownRule, item.Value, renamed)
		}

		if !slices.Contains(names, item.Value) {
			return fmt.Errorf("line %d: %s: %w %q; %s", item.Line, key, ErrUnknownRule, item.Value, suggest(item.Value, names, key+" takes "+strings.Join(names, ", ")))
		}
	}

	return nil
}

// ruleKinds turns rule names from treaty.yaml into the kinds of finding
// they stand for. decodeConfig has checked every name.
func ruleKinds(names []string) []string {
	result := []string{}
	for _, name := range names {
		for _, rule := range app.RuleNames {
			if rule.Name == name {
				result = append(result, rule.Kind)
			}
		}
	}

	return result
}

// value finds a mapping's value by key.
func value(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}

	return nil
}

// suggest names the known word closest to a typo, or says otherwise when
// none is close.
func suggest(word string, known []string, otherwise string) string {
	best, distance := "", 3
	for _, candidate := range known {
		if d := editDistance(word, candidate); d < distance {
			best, distance = candidate, d
		}
	}

	if best == "" {
		return otherwise
	}

	return fmt.Sprintf("did you mean %q?", best)
}

// editDistance counts the single-character insertions, deletions and
// substitutions that turn a into b.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}

		previous = current
	}

	return previous[len(b)]
}

// checkIgnores refuses an entry of rules.ignore that is not a mapping of
// exactly the ignore keys to text, names a kind of finding that cannot be
// ignored, has no reason, or repeats another entry's finding and target.
func checkIgnores(list *yaml.Node) error {
	if list == nil || list.Tag == "!!null" {
		return nil
	}

	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: %w: rules.ignore must be a list of entries, each with %s", list.Line, ErrIgnore, strings.Join(ignoreKeys, ", "))
	}

	var kinds []string
	for _, kind := range rules.FindingKinds {
		if app.Ignorable(kind) {
			kinds = append(kinds, kind)
		}
	}

	seen := map[[2]string]int{}
	for _, entry := range list.Content {
		if entry.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: %w: each entry of rules.ignore maps %s", entry.Line, ErrIgnore, strings.Join(ignoreKeys, ", "))
		}

		for i := 0; i+1 < len(entry.Content); i += 2 {
			key, text := entry.Content[i], entry.Content[i+1]
			if !slices.Contains(ignoreKeys, key.Value) {
				return fmt.Errorf("line %d: %w %q under rules.ignore; %s", key.Line, ErrUnknownKey, key.Value, suggest(key.Value, ignoreKeys, "an entry has "+strings.Join(ignoreKeys, ", ")))
			}

			if text.Kind != yaml.ScalarNode || strings.TrimSpace(text.Value) == "" {
				return fmt.Errorf("line %d: %w: %s must be text, and not empty", text.Line, ErrIgnore, key.Value)
			}
		}

		for _, key := range ignoreKeys {
			if value(entry, key) == nil {
				why := ""
				if key == "reason" {
					why = ": say why it is ignored, so a reviewer can judge it"
				}

				return fmt.Errorf("line %d: %w: the entry has no %s%s", entry.Line, ErrIgnore, key, why)
			}
		}

		finding := value(entry, "finding")
		if !slices.Contains(kinds, finding.Value) {
			return fmt.Errorf("line %d: %w: %q is not a kind of finding that can be ignored; %s", finding.Line, ErrIgnore, finding.Value, suggest(finding.Value, kinds, "it is one of "+strings.Join(kinds, ", ")))
		}

		key := [2]string{finding.Value, value(entry, "target").Value}
		if line, repeated := seen[key]; repeated {
			return fmt.Errorf("line %d: %w: line %d already ignores %s %s", entry.Line, ErrIgnore, line, key[0], key[1])
		}

		seen[key] = entry.Line
	}

	return nil
}

// ignores reads the entries of rules.ignore, which checkIgnores has
// checked.
func ignores(list *yaml.Node) (result []app.Ignore) {
	if list.Kind != yaml.SequenceNode {
		return nil
	}

	for _, entry := range list.Content {
		result = append(result, app.Ignore{
			Finding: value(entry, "finding").Value, Target: value(entry, "target").Value,
			Fingerprint: value(entry, "fingerprint").Value, Reason: value(entry, "reason").Value, Line: entry.Line,
		})
	}

	return result
}
