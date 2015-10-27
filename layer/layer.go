// Package layer is package for managing read only
// and read-write mounts on the union file system
// driver. Read-only mounts are refenced using a
// content hash and are protected from mutation in
// the exposed interface. The tar format is used
// to create read only layers and export both
// read only and writable layers. The exported
// tar data for a read only layer should match
// the tar used to create the layer.
package layer

import (
	"errors"
	"io"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/pkg/archive"
)

var (
	// ErrLayerDoesNotExist is used when an operation is
	// attempted on a layer which does not exist.
	ErrLayerDoesNotExist = errors.New("layer does not exist")

	// ErrLayerNotRetained is used when a release is
	// attempted on a layer which is not retained.
	ErrLayerNotRetained = errors.New("layer not retained")

	// ErrMountDoesNotExist is used when an operation is
	// attempted on a mount layer which does not exist.
	ErrMountDoesNotExist = errors.New("mount does not exist")

	// ErrActiveMount is used when an operation on a
	// mount is attempted but the layer is still
	// mounted and the operation cannot be performed.
	ErrActiveMount = errors.New("mount still active")

	// ErrNotMounted is used when requesting an active
	// mount but the layer is not mounted.
	ErrNotMounted = errors.New("not mounted")
)

// ID is the content-addressable ID of a layer.
type ID digest.Digest

// String returns a string rendition of a layer ID
func (id ID) String() string {
	return string(id)
}

// DiffID is the hash of an individual layer tar.
type DiffID digest.Digest

// String returns a string rendition of a layer DiffID
func (diffID DiffID) String() string {
	return string(diffID)
}

// TarStreamer represents an object which may
// have its contents exported as a tar stream.
type TarStreamer interface {
	// TarStream returns a tar archive stream
	// for the contents of a layer.
	TarStream() (io.Reader, error)
}

// Layer represents a read only layer
type Layer interface {
	TarStreamer

	// ID returns the content hash of the entire
	// layer chain. The hash chain is made up of
	// DiffID of top layer and all of its parents.
	ID() ID

	// DiffID returns the content hash of the layer
	// tar stream used to create this layer.
	DiffID() DiffID

	// Parent returns the next layer in the layer chain.
	Parent() Layer

	// Size returns the size of the entire layer chain. The size
	// is calculated from the total size of all files in the layers.
	Size() (int64, error)

	// DiffSize returns the size difference of the top layer
	// from parent layer.
	DiffSize() (int64, error)

	// Metadata returns the low level storage metadata associated
	// with layer.
	Metadata() (map[string]string, error)
}

// RWLayer represents a layer which is
// read and writable
type RWLayer interface {
	TarStreamer

	// Path returns the filesystem path to the writable
	// layer.
	Path() (string, error)

	// Parent returns the layer which the writable
	// layer was created from.
	Parent() Layer

	// Size represents the size of the writable layer
	// as calculated by the total size of the files
	// changed in the mutable layer.
	Size() (int64, error)
}

// Metadata holds information about a
// read only layer
type Metadata struct {
	// LayerID is the content hash of the layer
	LayerID ID

	// DiffID is the hash of the tar data used to
	// create the layer
	DiffID DiffID

	// Size is the size of the layer and all parents
	Size int64

	// DiffSize is the size of the top layer
	DiffSize int64
}

// MountInit is a function to initialize a
// writable mount. Changes made here will
// not be included in the Tar stream of the
// RWLayer.
type MountInit func(root string) error

// Store represents a backend for managing both
// read-only and read-write layers.
type Store interface {
	Register(io.Reader, ID) (Layer, error)
	Get(ID) (Layer, error)
	Release(Layer) ([]Metadata, error)

	Mount(id string, parent ID, label string, init MountInit) (RWLayer, error)
	Unmount(id string) error
	DeleteMount(id string) ([]Metadata, error)
	Changes(id string) ([]archive.Change, error)
}

// MetadataTransaction represents functions for setting layer metadata
// with a single transaction.
type MetadataTransaction interface {
	SetSize(int64) error
	SetParent(parent ID) error
	SetDiffID(DiffID) error
	SetCacheID(string) error
	TarSplitWriter() (io.WriteCloser, error)

	Commit(ID) error
	Cancel() error
	String() string
}

// MetadataStore represents a backend for persisting
// metadata about layers and providing the metadata
// for restoring a Store.
type MetadataStore interface {
	// StartTransaction starts an update for new metadata
	// which will be used to represent an ID on commit.
	StartTransaction() (MetadataTransaction, error)

	GetSize(ID) (int64, error)
	GetParent(ID) (ID, error)
	GetDiffID(ID) (DiffID, error)
	GetCacheID(ID) (string, error)
	TarSplitReader(ID) (io.ReadCloser, error)

	SetMountID(string, string) error
	SetInitID(string, string) error
	SetMountParent(string, ID) error

	GetMountID(string) (string, error)
	GetInitID(string) (string, error)
	GetMountParent(string) (ID, error)

	// List returns the full list of referened
	// read-only and read-write layers
	List() ([]ID, []string, error)

	Remove(ID) error
	RemoveMount(string) error
}

// CreateID returns ID for a layerDigest slice
func CreateID(dgsts []DiffID) ID {
	return createIDFromParent("", dgsts...)
}

func createIDFromParent(parent ID, dgsts ...DiffID) ID {
	if len(dgsts) == 0 {
		return parent
	}
	if parent == "" {
		return createIDFromParent(ID(dgsts[0]), dgsts[1:]...)
	}
	// H = "H(n-1) SHA256(n)"
	dgst, err := digest.FromBytes([]byte(string(parent) + " " + string(dgsts[0])))
	if err != nil {
		// Digest calculation is not expected to throw an error,
		// any error at this point is a program error
		panic(err)
	}
	return createIDFromParent(ID(dgst), dgsts[1:]...)
}
