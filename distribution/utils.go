package distribution

import (
	"github.com/docker/docker/layer"
)

// addLayerDiffIDs creates a slice containing all layer diff IDs for the given
// layer, ordered from base to top-most.
func addLayerDiffIDs(l layer.Layer) []layer.DiffID {
	parent := l.Parent()
	if parent == nil {
		return []layer.DiffID{l.DiffID()}
	}
	return append(addLayerDiffIDs(parent), l.DiffID())
}
