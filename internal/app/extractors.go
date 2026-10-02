package app

import "github.com/smarty/treaty/internal/graph"

// Extractors reads a tree with several languages' extractors into one
// graph.
type Extractors []SourceExtractor

// Extract runs every extractor over root and merges what they find.
//
// Parameters:
//   - root: the directory to read.
//
// Returns:
//   - result: the normalized graph, without layers.
//   - err: an extractor could not read the tree.
func (this Extractors) Extract(root string) (result *graph.Graph, err error) {
	result = graph.New()
	for _, extractor := range this {
		found, err := extractor.Extract(root)
		if err != nil {
			return nil, err
		}

		result.Merge(found)
	}

	result.Normalize()
	return result, nil
}

// Source reads one file under root through the first extractor that can.
//
// Parameters:
//   - root: the repository root.
//   - file: the path relative to root.
//
// Returns:
//   - result: the file's bytes.
//   - err: no extractor could read the file.
func (this Extractors) Source(root, file string) (result []byte, err error) {
	for _, extractor := range this {
		if result, err = extractor.Source(root, file); err == nil {
			return result, nil
		}
	}

	return nil, err
}
