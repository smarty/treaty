package filesystem

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrNoConfig = errors.New("no treaty.yaml")

// Place edits treaty.yaml so that one module resolves to a placement. It
// takes the module's exact path out of every list, then adds it to the
// target's list unless the remaining globs already place it there. Comments
// and everything else in the file are kept.
//
// Notes:
//   - An exact path wins over every wildcard (see rules.Architecture.Resolve),
//     so this works however broad the other globs are.
//   - A missing list for the target, such as an adapter side, is created.
//
// Parameters:
//   - modulePath: the module's path relative to the repository root.
//   - placement: the layer, and for a hexagonal adapter the side, to place
//     it in.
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
	path = filepath.Join(this.root, ConfigFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: run treaty init to declare an architecture first", ErrNoConfig)
	}

	if err != nil {
		return "", err
	}

	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("%s: %w", ConfigFile, err)
	}

	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("%w: %s is not a mapping", rules.ErrArchitecture, ConfigFile)
	}

	architecture, err := parseArchitecture(data)
	if err != nil {
		return "", err
	}

	root := document.Content[0]
	for _, list := range placementLists(root, architecture.Style) {
		removeScalar(list, modulePath)
	}

	text, architecture, err := encodeConfig(&document)
	if err != nil {
		return "", err
	}

	if architecture.Resolve(modulePath) != placement {
		appendScalar(targetList(root, architecture.Style, placement), modulePath)
		if text, architecture, err = encodeConfig(&document); err != nil {
			return "", err
		}
	}

	if got := architecture.Resolve(modulePath); got != placement {
		return "", fmt.Errorf("%w: %s would be placed in %s, not %s", rules.ErrArchitecture, modulePath, describe(got), describe(placement))
	}

	return path, os.WriteFile(path, text, 0o644)
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
	if placement.Side != "" {
		return placement.Layer + " (" + placement.Side + ")"
	}

	return placement.Layer
}

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
	var file configFile
	if err := yaml.Unmarshal(data, &file); err != nil {
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
// hexagonal adapter's two sides.
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
		case key == "layers" && value.Kind == yaml.MappingNode:
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
func targetList(root *yaml.Node, style string, placement rules.Placement) *yaml.Node {
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
