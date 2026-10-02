// Package htmlmap renders the interactive hexagon map as one self-contained
// HTML file with no server.
package htmlmap

import (
	"embed"
	"encoding/json"
	"strings"

	"github.com/smarty/treaty/internal/app"
)

// scripts are the page's script files in the order they run. They are
// joined into one script, so they share one scope, and a file may use what
// an earlier one declares when it runs.
var scripts = []string{
	"core.js",
	"references.js",
	"inspector.js",
	"controls.js",
	"moving.js",
	"pointer.js",
	"live.js",
	"files.js",
	"symbols.js",
	"tests.js",
	"panels.js",
}

//go:embed page
var assets embed.FS

// page is the whole page, its style and scripts inlined, so a rendered map
// is one self-contained file.
var page = assemble()

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
	}{view, ComputeLayout(view)})
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

// assemble builds the page from page/page.html, inlining page/style.css and
// the scripts.
//
// Returns:
//   - result: the page.
func assemble() (result string) {
	read := func(name string) string {
		data, err := assets.ReadFile("page/" + name)
		if err != nil {
			panic(err)
		}

		return string(data)
	}

	var script strings.Builder
	for _, name := range scripts {
		script.WriteString(read("js/" + name))
		script.WriteString("\n")
	}

	result = strings.Replace(read("page.html"), "/*STYLE*/", read("style.css"), 1)
	return strings.Replace(result, "/*SCRIPT*/", script.String(), 1)
}
