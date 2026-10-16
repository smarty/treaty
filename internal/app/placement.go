package app

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

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
//   - placement: the layer, and for a hexagonal adapter its side; for
//     vertical and modular, the boundary, or shared or composition; for
//     snowflake, the core of a star holding the module with that star,
//     the module's own spoke, or composition.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: the move is not possible, or treaty.yaml could not be written.
//
// Errors:
//   - ErrPreviewing: the map shows a previewed architecture, not
//     treaty.yaml's.
//   - ErrPlacement: there is no architecture, the layer or boundary is not
//     one of its own, the module is unknown, or, in a snowflake, the move
//     would put it in another directory's spoke or another star's core.
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
	if architecture.Style == rules.StyleNone {
		return "", fmt.Errorf("%w: there is no architecture, so there is nowhere to move a module; declare one in treaty.yaml", ErrPlacement)
	}

	module := analysis.head.Module(moduleID)
	if module == nil {
		return "", fmt.Errorf("%w: %w: %s", ErrPlacement, ErrUnknownTarget, moduleID)
	}

	if err := placeable(architecture, module.Path, placement); err != nil {
		return "", err
	}

	return this.config.Place(module.Path, placement)
}

// placeable reports whether an architecture has room for a placement of a
// module: its composition, or one of its layers with the right side; for
// vertical and modular, one of its boundaries, shared or composition; for
// snowflake, see starPlaceable.
func placeable(architecture rules.Architecture, modulePath string, placement rules.Placement) error {
	if architecture.Style == rules.StyleSnowflake {
		return starPlaceable(architecture, modulePath, placement)
	}

	if architecture.Style == rules.StyleVertical || architecture.Style == rules.StyleModular {
		switch {
		case placement.Boundary != "":
			for _, boundary := range architecture.Boundaries {
				if boundary.Name == placement.Boundary {
					return nil
				}
			}

			return fmt.Errorf("%w: there is no boundary %q", ErrPlacement, placement.Boundary)
		case placement.Side != "" || placement.Public:
			return fmt.Errorf("%w: a move sets a boundary, shared or composition, not a side or public API", ErrPlacement)
		case placement.Layer == graph.LayerComposition, placement.Layer == graph.LayerShared:
			return nil
		}

		return fmt.Errorf("%w: in the %s architecture a module moves to a boundary, shared or composition, not %q", ErrPlacement, architecture.Style, placement.Layer)
	}

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
	case placement.Boundary != "" || placement.Public:
		return fmt.Errorf("%w: a layer move sets no boundary or public API", ErrPlacement)
	}

	return nil
}

// starPlaceable reports whether a snowflake has room for a placement of a
// module. A module may move into the core of a star whose directory holds
// it, out of a core into its own directory's spoke, or to composition. A
// spoke is its directory, so a module cannot move to another spoke without
// moving its files, and a star's core lies inside the star.
func starPlaceable(architecture rules.Architecture, modulePath string, placement rules.Placement) error {
	switch {
	case placement.Side != "" || placement.Boundary != "" || placement.Public:
		return fmt.Errorf("%w: a snowflake move sets a star's core, a spoke or composition, not a side, boundary or public API", ErrPlacement)
	case placement.Layer == graph.LayerComposition:
		if placement.Star != "" || placement.Spoke != "" {
			return fmt.Errorf("%w: composition belongs to no star", ErrPlacement)
		}

		return nil
	case placement.Layer == graph.LayerCore:
		innermost := architecture.Innermost(modulePath)
		switch {
		case placement.Spoke != "":
			return fmt.Errorf("%w: a core has no spoke", ErrPlacement)
		case !slices.ContainsFunc(architecture.Stars, func(star rules.Star) bool { return star.Dir == placement.Star }):
			return fmt.Errorf("%w: there is no star %q", ErrPlacement, placement.Star)
		case innermost != placement.Star:
			return fmt.Errorf("%w: a star's core is inside its directory, and the innermost star holding %s is %s, not %s; drop it on the core of that star", ErrPlacement, modulePath, starOrNone(innermost), placement.Star)
		}

		return nil
	case placement.Layer == graph.LayerSpoke:
		own := architecture.Innermost(modulePath)
		if own == "" || own == modulePath {
			return fmt.Errorf("%w: %s is in no spoke; a star's own package is always in its core", ErrPlacement, modulePath)
		}

		child, _, _ := strings.Cut(strings.TrimPrefix(modulePath, strings.TrimPrefix(own+"/", "./")), "/")
		if spoke := path.Join(own, child); placement.Star != own || placement.Spoke != spoke {
			return fmt.Errorf("%w: a spoke is its directory: move the package's files into %s to put it there", ErrPlacement, placement.Spoke)
		}

		return nil
	}

	return fmt.Errorf("%w: in the snowflake architecture a module moves to a star's core, its own spoke or composition, not %q", ErrPlacement, placement.Layer)
}

// starOrNone names a star directory for a message, or says there is none.
func starOrNone(dir string) string {
	if dir == "" {
		return "none"
	}

	return dir
}

// RenameBoundary renames a boundary in treaty.yaml and rebuilds, so the map,
// the checks and agents follow. A name given on the map alone is not this:
// a boundary's name is the one treaty.yaml gives it.
//
// Parameters:
//   - from: the boundary's name.
//   - to: its new name.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: the rename is not possible, or treaty.yaml could not be written.
//
// Errors:
//   - ErrPreviewing: the map shows a previewed architecture, not
//     treaty.yaml's.
func (this *Live) RenameBoundary(from, to string) (path string, err error) {
	this.mutex.Lock()
	previewing := this.preview != "" && this.preview != this.configured
	this.mutex.Unlock()
	if previewing {
		return "", ErrPreviewing
	}

	if path, err = this.service.config.RenameBoundary(from, to); err != nil {
		return "", err
	}

	return path, this.Refresh()
}
