package image

import (
	"encoding/json"
	"io"
	"time"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/runconfig"
)

// ID is the content-addressable ID of an image.
type ID digest.Digest

func (id ID) String() string {
	return string(id)
}

// V1Image stores the V1 image configuration.
type V1Image struct {
	// ID a unique 64 character identifier of the image
	ID string `json:"id,omitempty"`
	// Parent id of the image
	Parent string `json:"parent,omitempty"`
	// Comment user added comment
	Comment string `json:"comment,omitempty"`
	// Created timestamp when image was created
	Created time.Time `json:"created"`
	// Container is the id of the container used to commit
	Container string `json:"container,omitempty"`
	// ContainerConfig  is the configuration of the container that is committed into the image
	ContainerConfig runconfig.Config `json:"container_config,omitempty"`
	// DockerVersion specifies version on which image is built
	DockerVersion string `json:"docker_version,omitempty"`
	// Author of the image
	Author string `json:"author,omitempty"`
	// Config is the configuration of the container received from the client
	Config *runconfig.Config `json:"config,omitempty"`
	// Architecture is the hardware that the image is build and runs on
	Architecture string `json:"architecture,omitempty"`
	// OS is the operating system used to build and run the image
	OS string `json:"os,omitempty"`
	// Size is the total size of the image including all layers it is composed of
	Size int64 `json:",omitempty"`
}

// Image stores the image configuration
type Image struct {
	V1Image
	ID      ID        `json:"id,omitempty"`
	Parent  ID        `json:"parent,omitempty"`
	RootFS  *RootFS   `json:"rootfs,omitempty"`
	History []History `json:"history,omitempty"`

	// rawJSON caches the immutable JSON associated with this image.
	rawJSON []byte
}

// RootFS describes images root filesystem
// This is currently a placeholder that only supports layers. In the future
// this can be made into a interface that supports different implementaions.
type RootFS struct {
	Type    string         `json:"type"`
	DiffIDs []layer.DiffID `json:"diff_ids,omitempty"`
}

// RawJSON returns the immutable JSON associated with the image.
func (img *Image) RawJSON() []byte {
	return img.rawJSON
}

// GetTopLayerID returns the top layer ID for this image.
func (img *Image) GetTopLayerID() layer.ID {
	return layer.CreateID(img.RootFS.DiffIDs)
}

// History stores build commands that were used to create an image
type History struct {
	// Created timestamp for build point
	Created time.Time `json:"created"`
	// Author of the build point
	Author string `json:"author,omitempty"`
	// CreatedBy keeps the Dockerfile command used while building image.
	CreatedBy string `json:"created_by,omitempty"`
	// Comment is custom mesage set by the user when creating the image.
	Comment string `json:"comment,omitempty"`
	// Size in bytes how much data was added with the layer.
	Size int64 `json:"size,omitempty"`
}

// Exporter provides interface for exporting and importing images
type Exporter interface {
	Load(io.ReadCloser, io.Writer) error
	// Load(net.Context, io.ReadCloser, <- chan StatusMessage) error
	Save([]string, io.Writer) error
}

// NewFromJSON creates an Image configuration from json.
// This shouldn't be used directly - it's for unit tests.
func NewFromJSON(src []byte) (*Image, error) {
	ret := &Image{}

	// FIXME: Is there a cleaner way to "purify" the input json?
	if err := json.Unmarshal(src, ret); err != nil {
		return nil, err
	}

	ret.rawJSON = src

	return ret, nil
}
