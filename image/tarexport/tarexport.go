package tarexport

import (
	"errors"

	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/tag"
)

const (
	manifestFileName           = "manifest.json"
	legacyLayerFileName        = "layer.tar"
	legacyConfigFileName       = "json"
	legacyVersionFileName      = "VERSION"
	legacyRepositoriesFileName = "repositories"
)

type manifestItem struct {
	Config   string
	RepoTags []string
	Layers   []string
}

type tarexporter struct {
	is image.Store
	ls layer.Store
	ts tag.Store
}

// NewTarExporter returns new ImageExporter for tar packages
func NewTarExporter(is image.Store, ls layer.Store, ts tag.Store) (image.Exporter, error) {
	if is == nil || ls == nil || ts == nil {
		return nil, errors.New("Invalid tarexporter configuration")
	}
	return &tarexporter{
		is: is,
		ls: ls,
		ts: ts,
	}, nil
}
