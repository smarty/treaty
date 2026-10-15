package htmlmap

import "github.com/smarty/treaty/internal/adapters/gitvcs"

// demo uses another adapter, which the hexagonal architecture forbids, to
// show the treaty job's annotations. Not for merging.
var demo = gitvcs.New
