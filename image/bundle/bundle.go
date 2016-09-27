package bundle

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/image"
)

// ID is the content-addressable ID of a bundle.
type ID digest.Digest

func (id ID) String() string {
	return id.Digest().String()
}

func (id ID) Digest() digest.Digest {
	return digest.Digest(id)
}

func IDFromDigest(digest digest.Digest) ID {
	return ID(digest)
}

// Bundle stores the bundle configuration
type Bundle struct {
	Services []ServiceSpec

	// Created timestamp when image was created
	Created time.Time `json:"created"`
	// DockerVersion specifies version on which image is built
	DockerVersion string `json:"docker_version,omitempty"`
	// Labels is a list of labels set to this bundle
	Labels map[string]string

	// rawJSON caches the immutable JSON associated with this image.
	rawJSON    []byte
	computedID ID
}

// Port is a port as defined in a bundlefile
type Port struct {
	Protocol string
	Port     uint32
}

// ServiceSpec TODO: taken from bundlefile, should probably be swarm engine-api types instead
type ServiceSpec struct {
	Image      image.ID
	Name       string
	Command    []string          `json:",omitempty"`
	Args       []string          `json:",omitempty"`
	Env        []string          `json:",omitempty"`
	Labels     map[string]string `json:",omitempty"`
	Ports      []Port            `json:",omitempty"`
	WorkingDir string            `json:",omitempty"`
	User       string            `json:",omitempty"`
	Networks   []string          `json:",omitempty"`
}

// RawJSON returns the immutable JSON associated with the image.
func (img *Bundle) RawJSON() []byte {
	return img.rawJSON
}

// ID returns the image's content-addressable ID.
func (img *Bundle) ID() ID {
	return img.computedID
}

// NewFromJSON creates a bundle configuration from json.
func NewFromJSON(src []byte) (*Bundle, error) {
	bundle := &Bundle{}

	if err := json.Unmarshal(src, bundle); err != nil {
		return nil, err
	}
	if len(bundle.Services) == 0 {
		return nil, errors.New("Invalid bundle JSON, no services specified.")
	}

	bundle.rawJSON = src

	return bundle, nil
}
