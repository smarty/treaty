package app

import "errors"

var (
	ErrConfigChanged = errors.New("treaty.yaml changed since it was read; reload it before saving")
	ErrConfigInvalid = errors.New("treaty.yaml is invalid")
)

// ConfigText is treaty.yaml as it is on disk, for the person to edit.
type ConfigText struct {
	// Text is the file's text, empty when there is no file.
	Text string `json:"text"`

	// Found is false when there is no treaty.yaml.
	Found bool `json:"found"`
}

// ConfigText reads treaty.yaml as it is on disk.
//
// Returns:
//   - result: the file's text and whether it exists.
//   - err: the file could not be read.
func (this *Live) ConfigText() (result ConfigText, err error) {
	result.Text, result.Found, err = this.service.config.Text()
	return result, err
}

// SaveConfig replaces treaty.yaml with text the person wrote, then
// rebuilds the map. The text must load as treaty.yaml, and the file must
// still hold what the person started from, so a save never discards a
// change made elsewhere, such as by an agent or a move on the map.
//
// Parameters:
//   - text: the new treaty.yaml.
//   - expected: the text the person started from, empty when there was no
//     file.
//
// Returns:
//   - path: where treaty.yaml was written.
//   - err: the text is invalid, the file changed, or the write or rebuild
//     failed.
//
// Errors:
//   - ErrConfigInvalid: the text does not load as treaty.yaml; the message
//     names the line.
//   - ErrConfigChanged: the file no longer holds expected.
func (this *Live) SaveConfig(text, expected string) (path string, err error) {
	if path, err = this.service.config.Save(text, expected); err != nil {
		return "", err
	}

	return path, this.Refresh()
}
