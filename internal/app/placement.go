package app

import (
	"errors"
	"fmt"
	"slices"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var (
	ErrPlacement  = errors.New("cannot place the module there")
	ErrPreviewing = errors.New("the map previews another architecture; keep or adopt it before moving modules between layers")
)

// Positions reads where the person put modules on the map.
//
// Returns:
//   - result: each architecture's module positions.
//   - err: the saved positions could not be read.
func (this *Live) Positions() (result Positions, err error) {
	return this.service.workspace.Positions()
}

// Reclassify moves a module to another layer by editing treaty.yaml, then
// rebuilds, so the map, the rules and every check follow.
//
// Parameters:
//   - module: the module id.
//   - placement: the layer, and for a hexagonal adapter its side.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: the move is not possible, or treaty.yaml could not be written.
//
// Errors:
//   - ErrPreviewing: the map shows a previewed architecture, not
//     treaty.yaml's.
//   - ErrPlacement: the architecture does not place modules by layer, the
//     layer is not one of its layers, or the module is unknown.
//   - ErrNotReady: the graph has not been built yet.
func (this *Live) Reclassify(module string, placement rules.Placement) (path string, err error) {
	this.mutex.Lock()
	previewing := this.preview != "" && this.preview != this.configured
	this.mutex.Unlock()
	if previewing {
		return "", ErrPreviewing
	}

	current, err := this.current()
	if err != nil {
		return "", err
	}

	if path, err = this.service.reclassify(current, module, placement); err != nil {
		return "", err
	}

	return path, this.Refresh()
}

// SetPosition saves where the person put a module on the map drawn in one
// architecture, or forgets it, so the module goes back to its computed
// place.
//
// Parameters:
//   - architecture: the architecture the map was drawn in.
//   - module: the module id.
//   - position: where the module goes; nil to forget it.
//
// Returns:
//   - result: every saved position after the change.
//   - err: the architecture is unknown, or the positions could not be
//     read or written.
//
// Errors:
//   - rules.ErrArchitecture: architecture is not one of rules.Styles.
func (this *Live) SetPosition(architecture, module string, position *Position) (result Positions, err error) {
	if !slices.Contains(rules.Styles, architecture) {
		return nil, fmt.Errorf("%w: unknown architecture %q", rules.ErrArchitecture, architecture)
	}

	this.positions.Lock()
	defer this.positions.Unlock()
	result, err = this.service.workspace.Positions()
	if err != nil {
		return nil, err
	}

	if result == nil {
		result = Positions{}
	}

	if position == nil {
		delete(result[architecture], module)
		if len(result[architecture]) == 0 {
			delete(result, architecture)
		}
	} else {
		if result[architecture] == nil {
			result[architecture] = map[string]Position{}
		}

		result[architecture][module] = *position
	}

	return result, this.service.workspace.SavePositions(result)
}

// reclassify checks that a module can move to a placement under the
// analysis's architecture and edits the config to put it there.
func (this *Service) reclassify(analysis *analysis, moduleID string, placement rules.Placement) (path string, err error) {
	architecture := analysis.config.Architecture
	switch architecture.Style {
	case rules.StyleHexagonal, rules.StyleClean, rules.StyleLayered:
	default:
		return "", fmt.Errorf("%w: the %s architecture does not place modules in layers by treaty.yaml alone; a module stays where its directory puts it", ErrPlacement, architecture.Style)
	}

	module := analysis.head.Module(moduleID)
	if module == nil {
		return "", fmt.Errorf("%w: %w: %s", ErrPlacement, ErrUnknownTarget, moduleID)
	}

	if err := placeable(architecture, placement); err != nil {
		return "", err
	}

	return this.config.Place(module.Path, placement)
}

// placeable reports whether an architecture has room for a placement: its
// composition, or one of its layers with the right side.
func placeable(architecture rules.Architecture, placement rules.Placement) error {
	layers := architecture.LayerNames()
	switch architecture.Style {
	case rules.StyleHexagonal:
		layers = rules.HexagonalLayers
	case rules.StyleClean:
		layers = rules.CleanLayers
	}

	sided := architecture.Style == rules.StyleHexagonal && placement.Layer == graph.LayerAdapter
	switch {
	case placement.Layer != graph.LayerComposition && !slices.Contains(layers, placement.Layer):
		return fmt.Errorf("%w: the %s architecture has no %q layer", ErrPlacement, architecture.Style, placement.Layer)
	case sided && placement.Side != graph.SideDriving && placement.Side != graph.SideDriven:
		return fmt.Errorf("%w: an adapter is driving or driven, not %q", ErrPlacement, placement.Side)
	case !sided && placement.Side != "":
		return fmt.Errorf("%w: only a hexagonal adapter has a side", ErrPlacement)
	case placement.Slice != "" || placement.Public:
		return fmt.Errorf("%w: a layer move sets no slice or public API", ErrPlacement)
	}

	return nil
}
