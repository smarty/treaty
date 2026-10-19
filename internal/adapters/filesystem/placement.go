package filesystem

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrNoConfig = errors.New("no treaty.yaml")

// Place edits treaty.yaml so that one module resolves to a placement. It
// takes the module's exact path out of every list, then adds it to the
// target's list unless the remaining globs already place it there. Comments
// and everything else in the file are kept. For vertical and modular, the
// target is a boundary, shared or composition, and a module keeps whatever
// layer it has inside its boundary. For snowflake, see placeInStar.
//
// Notes:
//   - An exact path wins over every wildcard (see rules.Architecture.Resolve),
//     so this works however broad the other globs are.
//   - A missing list for the target, such as an adapter side, is created;
//     a boundary never is.
//
// Parameters:
//   - modulePath: the module's path relative to the repository root.
//   - placement: the layer, and for a hexagonal adapter the side, to place
//     it in; for vertical and modular, the boundary, or the layer shared or
//     composition; for snowflake, contracts, assembler or engine and the star,
//     or leaf and the spoke.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: treaty.yaml is missing or invalid, or the edit would not place
//     the module there.
//
// Errors:
//   - ErrNoConfig: there is no treaty.yaml to edit.
//   - rules.ErrArchitecture: the file's architecture is invalid, or the
//     placement is not one it can hold.
func (this *Config) Place(modulePath string, placement rules.Placement) (path string, err error) {
	path, document, architecture, err := this.document()
	if err != nil {
		return "", err
	}

	root := document.Content[0]
	if architecture.Style == rules.StyleSnowflake {
		return placeInStar(path, document, architecture, modulePath, placement)
	}

	// A module already where the move puts it needs no edit.
	if fits(architecture, architecture.Resolve(modulePath), placement) {
		return path, nil
	}

	for _, list := range placementLists(root, architecture.Style) {
		removeScalar(list, modulePath)
	}

	text, architecture, err := encodeConfig(document)
	if err != nil {
		return "", err
	}

	if !fits(architecture, architecture.Resolve(modulePath), placement) {
		list, err := targetList(root, architecture.Style, placement)
		if err != nil {
			return "", err
		}

		appendScalar(list, modulePath)
		if text, architecture, err = encodeConfig(document); err != nil {
			return "", err
		}
	}

	if got := architecture.Resolve(modulePath); !fits(architecture, got, placement) {
		return "", fmt.Errorf("%w: %s would be placed in %s, not %s", rules.ErrArchitecture, modulePath, describe(got), describe(placement))
	}

	return path, writeChanged(path, text)
}

// placeInStar edits a snowflake's treaty.yaml by directory, never by
// resolving: it takes the module's directory out of every star's contracts,
// assemblers and engines, then, for a core target, adds it to that list of
// its star, creating the star's mapping or the list as a flow list, in the
// order contracts, assemblers, engines. A star that still names its
// assemblers orchestrators keeps that key. A leaf target only takes it out.
//
// Notes:
//   - Refused: composition, a star treaty.yaml does not list, a star other
//     than the innermost one holding the module, a package inside a spoke
//     into the core, a package beneath a core package out to a spoke, a
//     star's own package out of its core, and another spoke than the
//     module's own, since a spoke is its directory.
func placeInStar(path string, document *yaml.Node, architecture rules.Architecture, modulePath string, placement rules.Placement) (string, error) {
	own, position := architecture.Innermost(modulePath), architecture.Position(modulePath)
	nested := modulePath != own && position != modulePath
	underCore := nested && architecture.Resolve(position).Entry == position
	var key string
	switch placement.Layer {
	case graph.LayerContracts, graph.LayerAssembler, graph.LayerEngine:
		key = map[string]string{graph.LayerContracts: "contracts", graph.LayerAssembler: "assemblers", graph.LayerEngine: "engines"}[placement.Layer]
		switch {
		case !slices.ContainsFunc(architecture.Stars, func(star rules.Star) bool { return star.Dir == placement.Star }):
			return "", fmt.Errorf("%w: no star %q in %s", rules.ErrArchitecture, placement.Star, ConfigFile)
		case placement.Star != own:
			return "", fmt.Errorf("%w: a core package is a child of its own star: drop it on the core of star %s", rules.ErrArchitecture, own)
		case nested && !underCore:
			return "", fmt.Errorf("%w: spoke %s holds it, and a core package is never part of a spoke: move %s into the core instead", rules.ErrArchitecture, position, position)
		}
	case graph.LayerLeaf:
		switch {
		case modulePath == own:
			return "", fmt.Errorf("%w: a star's own package is always in its core: drop it on its contracts, assemblers or engines", rules.ErrArchitecture)
		case underCore:
			return "", fmt.Errorf("%w: it lies beneath core package %s, so it is never a spoke: list it in the core, or move its files out of %s", rules.ErrArchitecture, position, position)
		case placement.Spoke != "" && placement.Spoke != position:
			return "", fmt.Errorf("%w: a spoke is its directory: move the package's files into %s to put it there", rules.ErrArchitecture, placement.Spoke)
		}
	case graph.LayerComposition:
		return "", fmt.Errorf("%w: composition does not apply to the snowflake architecture: assemblers do its work, so move the module among the assemblers of its star", rules.ErrArchitecture)
	default:
		return "", fmt.Errorf("%w: in the snowflake architecture a module moves into its star's contracts, assemblers or engines, or out to its own spoke, not %s", rules.ErrArchitecture, describe(placement))
	}

	root := document.Content[0]
	if got := architecture.Resolve(modulePath); (key == "" && got.Entry != modulePath) || (key != "" && got.Layer == placement.Layer && got.Entry == modulePath) {
		return path, nil
	}

	for _, list := range placementLists(root, rules.StyleSnowflake) {
		removeScalar(list, modulePath)
	}

	if key != "" {
		star := starMapping(mappingValue(root, "stars"), own)
		if key == "assemblers" && mappingValue(star, "orchestrators") != nil {
			key = "orchestrators"
		}

		before := ""
		for _, later := range map[string][]string{"contracts": {"assemblers", "orchestrators", "engines"}, "assemblers": {"engines"}, "orchestrators": {"engines"}}[key] {
			if before == "" && mappingValue(star, later) != nil {
				before = later
			}
		}

		appendScalar(child(star, key, yaml.SequenceNode, before), modulePath)
	}

	text, _, err := encodeConfig(document)
	if err != nil {
		return "", err
	}

	return path, writeChanged(path, text)
}

// RenameBoundary renames a boundary in treaty.yaml, keeping its globs, its
// place among the boundaries and every comment.
//
// Parameters:
//   - from: the boundary's name.
//   - to: its new name.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: treaty.yaml is missing or invalid, there is no boundary from, or
//     the new name is taken or not a valid name.
//
// Errors:
//   - ErrNoConfig: there is no treaty.yaml to edit.
//   - rules.ErrArchitecture: there is no such boundary, or the renamed file
//     would not load.
func (this *Config) RenameBoundary(from, to string) (path string, err error) {
	path, document, _, err := this.document()
	if err != nil {
		return "", err
	}

	boundaries := mappingValue(document.Content[0], "boundaries")
	if boundaries == nil || boundaries.Kind != yaml.MappingNode {
		return "", fmt.Errorf("%w: %s has no boundaries", rules.ErrArchitecture, ConfigFile)
	}

	var key *yaml.Node
	for i := 0; i+1 < len(boundaries.Content); i += 2 {
		switch boundaries.Content[i].Value {
		case from:
			key = boundaries.Content[i]
		case to:
			return "", fmt.Errorf("%w: there is already a boundary %q", rules.ErrArchitecture, to)
		}
	}

	if key == nil {
		return "", fmt.Errorf("%w: no boundary %q in %s", rules.ErrArchitecture, from, ConfigFile)
	}

	if from == to {
		return path, nil
	}

	if strings.TrimSpace(to) == "" || strings.TrimSpace(to) != to {
		return "", fmt.Errorf("%w: a boundary's name may not be empty or start or end with a space", rules.ErrArchitecture)
	}

	key.Value = to
	text, _, err := encodeConfig(document)
	if err != nil {
		return "", err
	}

	return path, writeChanged(path, text)
}

// Ignore adds an entry to rules.ignore, or, when one already names the
// finding and target, gives it the new fingerprint and reason in place.
// Comments and everything else in the file are kept.
//
// Parameters:
//   - ignore: the entry; its Line is not used.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: treaty.yaml is missing or invalid, or the entry is not one it
//     would load.
//
// Errors:
//   - ErrNoConfig: there is no treaty.yaml to edit.
//   - ErrIgnore: the entry is incomplete or names a finding that cannot be
//     ignored.
func (this *Config) Ignore(ignore app.Ignore) (path string, err error) {
	path, document, _, err := this.document()
	if err != nil {
		return "", err
	}

	list := child(child(document.Content[0], "rules", yaml.MappingNode, ""), "ignore", yaml.SequenceNode, "")
	if len(list.Content) == 0 {
		list.Style = 0
	}

	text := func(value string, style yaml.Style) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
	}

	entry := ignoreEntry(list, ignore.Finding, ignore.Target)
	if entry == nil {
		entry = &yaml.Node{Kind: yaml.MappingNode}
		for _, key := range ignoreKeys {
			entry.Content = append(entry.Content, text(key, 0), text("", 0))
		}

		list.Content = append(list.Content, entry)
	}

	// A fingerprint of digits alone would read as a number, so it is
	// always quoted.
	for key, value := range map[string]*yaml.Node{"finding": text(ignore.Finding, 0), "target": text(ignore.Target, 0), "fingerprint": text(ignore.Fingerprint, yaml.DoubleQuotedStyle), "reason": text(ignore.Reason, 0)} {
		for i := 0; i+1 < len(entry.Content); i += 2 {
			if entry.Content[i].Value == key {
				value.LineComment = entry.Content[i+1].LineComment
				entry.Content[i+1] = value
			}
		}
	}

	data, _, err := encodeConfig(document)
	if err != nil {
		return "", err
	}

	return path, writeChanged(path, data)
}

// Unignore removes the entry of rules.ignore that names a finding and
// target, if there is one.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: treaty.yaml is missing or invalid.
//
// Errors:
//   - ErrNoConfig: there is no treaty.yaml to edit.
func (this *Config) Unignore(finding, target string) (path string, err error) {
	path, document, _, err := this.document()
	if err != nil {
		return "", err
	}

	list := mappingValue(mappingValue(document.Content[0], "rules"), "ignore")
	entry := ignoreEntry(list, finding, target)
	if entry == nil {
		return path, nil
	}

	list.Content = slices.DeleteFunc(list.Content, func(item *yaml.Node) bool { return item == entry })
	data, _, err := encodeConfig(document)
	if err != nil {
		return "", err
	}

	return path, writeChanged(path, data)
}

// ignoreEntry finds the entry of rules.ignore for a finding and target.
func ignoreEntry(list *yaml.Node, finding, target string) *yaml.Node {
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}

	for _, entry := range list.Content {
		if entry.Kind == yaml.MappingNode {
			if kind, at := mappingValue(entry, "finding"), mappingValue(entry, "target"); kind != nil && at != nil && kind.Value == finding && at.Value == target {
				return entry
			}
		}
	}

	return nil
}

// writeChanged writes treaty.yaml only when its text would change, so an
// edit that changes nothing leaves the file, and the repository, as they
// were.
func writeChanged(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return nil
	}

	return os.WriteFile(path, data, 0o644)
}

// document reads treaty.yaml as a YAML tree to edit, with the architecture
// it declares.
func (this *Config) document() (path string, document *yaml.Node, architecture rules.Architecture, err error) {
	path = filepath.Join(this.root, ConfigFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, architecture, fmt.Errorf("%w: run treaty init to declare an architecture first", ErrNoConfig)
	}

	if err != nil {
		return "", nil, architecture, err
	}

	document = &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return "", nil, architecture, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return "", nil, architecture, fmt.Errorf("%w: %s is not a mapping", rules.ErrArchitecture, ConfigFile)
	}

	architecture, err = parseArchitecture(data)
	return path, document, architecture, err
}

// appendScalar adds a value to a sequence in the style of its other items.
func appendScalar(list *yaml.Node, value string) {
	style := yaml.DoubleQuotedStyle
	if len(list.Content) > 0 {
		style = list.Content[len(list.Content)-1].Style
	}

	list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style})
}

// child finds a mapping's value by key, creating it as the given kind when
// it is missing. A new key goes before the key named before, when there is
// one, so composition lands above layers.
func child(mapping *yaml.Node, key string, kind yaml.Kind, before string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			value := mapping.Content[i+1]
			if value.Kind != kind && value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
				value.Kind, value.Tag, value.Value = kind, "", ""
			}

			return value
		}
	}

	value := &yaml.Node{Kind: kind}
	if kind == yaml.SequenceNode {
		value.Style = yaml.FlowStyle
	}

	pair := []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value}
	at := len(mapping.Content)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if before != "" && mapping.Content[i].Value == before {
			at = i
		}
	}

	mapping.Content = append(mapping.Content[:at], append(pair, mapping.Content[at:]...)...)
	return value
}

// describe names a placement for an error message.
func describe(placement rules.Placement) string {
	switch {
	case placement.Layer == graph.LayerContracts, placement.Layer == graph.LayerAssembler, placement.Layer == graph.LayerEngine:
		return placement.Layer + " " + placement.Entry
	case placement.Layer == graph.LayerLeaf:
		return "leaf " + placement.Spoke
	case len(placement.Ambiguous) > 0:
		return "no boundary, claimed equally by " + strings.Join(placement.Ambiguous, " and ")
	case placement.Boundary != "":
		return "boundary " + placement.Boundary
	case placement.Side != "":
		return placement.Layer + " (" + placement.Side + ")"
	}

	return placement.Layer
}

// fits reports whether a module placed at got is where the move wanted it.
// In vertical and modular a move sets the boundary, shared or composition;
// the layer inside the boundary and its public API follow from the globs.
func fits(architecture rules.Architecture, got, want rules.Placement) bool {
	if !bounded(architecture.Style) {
		return got.Same(want)
	}

	if want.Boundary != "" {
		return got.Boundary == want.Boundary && len(got.Ambiguous) == 0
	}

	return got.Layer == want.Layer && got.Boundary == ""
}

// starMapping finds the mapping of the star with a directory, whose key may
// be spelled with a trailing slash or a leading ./. A star with no value yet,
// such as internal/gate:, gets an empty mapping.
func starMapping(stars *yaml.Node, dir string) *yaml.Node {
	if stars == nil || stars.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(stars.Content); i += 2 {
		if key, value := stars.Content[i].Value, stars.Content[i+1]; key != "" && path.Clean(key) == dir {
			if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
				value.Kind, value.Tag, value.Value = yaml.MappingNode, "", ""
			}

			return value
		}
	}

	return nil
}

func bounded(style string) bool { return style == rules.StyleVertical || style == rules.StyleModular }

// encodeConfig renders the document and parses it back, so an edit is
// checked exactly as treaty will read it.
func encodeConfig(document *yaml.Node) (text []byte, architecture rules.Architecture, err error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, architecture, err
	}

	if err := encoder.Close(); err != nil {
		return nil, architecture, err
	}

	architecture, err = parseArchitecture(buffer.Bytes())
	return buffer.Bytes(), architecture, err
}

// parseArchitecture reads the architecture a treaty.yaml declares.
func parseArchitecture(data []byte) (rules.Architecture, error) {
	file, _, err := decodeConfig(data)
	if err != nil {
		return rules.Architecture{}, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	architecture, err := file.architecture()
	if err != nil {
		return architecture, fmt.Errorf("%s: %w", ConfigFile, err)
	}

	return architecture, nil
}

// placementLists finds every list of globs that places modules by layer:
// composition, at the top or under layers, and each layer's, with a
// hexagonal adapter's two sides. For vertical and modular they are
// composition, shared and each boundary's; layers inside boundaries are
// left alone. For snowflake they are each star's contracts, assemblers,
// engines and orchestrators.
func placementLists(root *yaml.Node, style string) (result []*yaml.Node) {
	sequences := func(mapping *yaml.Node) {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			value := mapping.Content[i+1]
			switch {
			case value.Kind == yaml.SequenceNode:
				result = append(result, value)
			case value.Kind == yaml.MappingNode && style == rules.StyleHexagonal && mapping.Content[i].Value == graph.LayerAdapter:
				for j := 1; j < len(value.Content); j += 2 {
					if value.Content[j].Kind == yaml.SequenceNode {
						result = append(result, value.Content[j])
					}
				}
			}
		}
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		switch key, value := root.Content[i].Value, root.Content[i+1]; {
		case key == "composition" && value.Kind == yaml.SequenceNode:
			result = append(result, value)
		case bounded(style) && key == "shared" && value.Kind == yaml.SequenceNode:
			result = append(result, value)
		case bounded(style) && key == "boundaries" && value.Kind == yaml.MappingNode:
			sequences(value)
		case style == rules.StyleSnowflake && key == "stars" && value.Kind == yaml.MappingNode:
			for j := 1; j < len(value.Content); j += 2 {
				if value.Content[j].Kind == yaml.MappingNode {
					sequences(value.Content[j])
				}
			}
		case !bounded(style) && key == "layers" && value.Kind == yaml.MappingNode:
			sequences(value)
		}
	}

	return result
}

// removeScalar drops every item of a sequence equal to value.
func removeScalar(list *yaml.Node, value string) {
	kept := list.Content[:0]
	for _, item := range list.Content {
		if item.Kind != yaml.ScalarNode || item.Value != value {
			kept = append(kept, item)
		}
	}

	list.Content = kept
}

// targetList finds or creates the list that places modules in a placement.
// A boundary must exist already; a move never creates one.
func targetList(root *yaml.Node, style string, placement rules.Placement) (*yaml.Node, error) {
	if bounded(style) {
		switch {
		case placement.Boundary != "":
			boundaries := mappingValue(root, "boundaries")
			if list := mappingValue(boundaries, placement.Boundary); list != nil && list.Kind == yaml.SequenceNode {
				return list, nil
			}

			return nil, fmt.Errorf("%w: no boundary %q in %s", rules.ErrArchitecture, placement.Boundary, ConfigFile)
		case placement.Layer == graph.LayerShared:
			return child(root, "shared", yaml.SequenceNode, "boundaries"), nil
		case placement.Layer == graph.LayerComposition:
			return child(root, "composition", yaml.SequenceNode, "shared"), nil
		}

		return nil, fmt.Errorf("%w: in the %s architecture a module moves to a boundary, shared or composition, not %s", rules.ErrArchitecture, style, describe(placement))
	}

	return layerTarget(root, style, placement), nil
}

// mappingValue finds a mapping's value by key, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}

	return nil
}

// layerTarget finds or creates the list for a placement in a layer
// architecture.
func layerTarget(root *yaml.Node, style string, placement rules.Placement) *yaml.Node {
	if placement.Layer == graph.LayerComposition {
		if style == rules.StyleHexagonal {
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i].Value == "layers" && root.Content[i+1].Kind == yaml.MappingNode {
					for j := 0; j+1 < len(root.Content[i+1].Content); j += 2 {
						if root.Content[i+1].Content[j].Value == graph.LayerComposition {
							return child(root.Content[i+1], graph.LayerComposition, yaml.SequenceNode, "")
						}
					}
				}
			}
		}

		return child(root, "composition", yaml.SequenceNode, "layers")
	}

	layers := child(root, "layers", yaml.MappingNode, "rules")
	if style == rules.StyleHexagonal && placement.Layer == graph.LayerAdapter {
		return child(child(layers, graph.LayerAdapter, yaml.MappingNode, ""), placement.Side, yaml.SequenceNode, "")
	}

	return child(layers, placement.Layer, yaml.SequenceNode, "")
}
