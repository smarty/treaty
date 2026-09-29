// Package htmlmap renders the interactive hexagon map as one self-contained
// HTML file with no server.
package htmlmap

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/smarty/treaty/internal/app"
)

//go:embed page.html
var page string

// Renderer draws a MapView as HTML.
type Renderer struct{}

// New creates a renderer.
//
// Returns:
//   - result: the renderer.
func New() *Renderer {
	return &Renderer{}
}

// Page returns the live page. With no data embedded, it loads the view from
// the server that serves it and follows its updates.
//
// Returns:
//   - result: the HTML page.
func (this *Renderer) Page() []byte {
	return []byte(page)
}

// Payload lays out the modules and encodes the view and layout as the page
// reads them.
//
// Parameters:
//   - view: everything to draw.
//
// Returns:
//   - result: the JSON payload.
//   - err: the view could not be encoded.
func (this *Renderer) Payload(view app.MapView) (result []byte, err error) {
	return json.Marshal(struct {
		app.MapView
		Layout Layout `json:"layout"`
	}{view, ComputeLayout(view.Modules)})
}

// Render lays out the modules and embeds the view and layout in the page.
//
// Parameters:
//   - view: everything to draw.
//
// Returns:
//   - result: the HTML page.
//   - err: the view could not be encoded.
func (this *Renderer) Render(view app.MapView) (result []byte, err error) {
	data, err := this.Payload(view)
	if err != nil {
		return nil, err
	}

	return []byte(strings.Replace(page, "/*DATA*/null", string(data), 1)), nil
}
