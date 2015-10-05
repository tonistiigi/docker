package layers

import (
	"errors"
	"io"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stringid"
)

var (
	ErrLayerDoesNotExist = errors.New("layer does not exist")
)

type LayerAddress digest.Digest

type TarStreamer interface {
	TarStream() (io.Reader, error)
}

// Layer represents a read only layer
type Layer interface {
	TarStreamer
	Address() LayerAddress
	Parent() (Layer, error)
	Size() (int64, error)
}

// RWLayer represents a layer which is
// read and writable
type RWLayer interface {
	TarStreamer
	Path() (string, error)
	Parent() (Layer, error)
}

type LayerStore interface {
	Register(io.Reader, LayerAddress) (Layer, error)
	Get(LayerAddress) (Layer, error)
	Put(LayerAddress) error

	Mount(id string, parent LayerAddress) (RWLayer, error)
	Unmount(id string) error
}

type tarStreamer func() (io.Reader, error)

type cacheLayer struct {
	tarStreamer
	address LayerAddress
	parent  *cacheLayer
	cacheID string
	size    int64
}

func (cl *cacheLayer) TarStream() (io.Reader, error) {
	return cl.tarStreamer()
}

func (cl *cacheLayer) Address() LayerAddress {
	return cl.address
}

func (cl *cacheLayer) Parent() (Layer, error) {
	return cl.parent, nil
}

func (cl *cacheLayer) Size() (int64, error) {
	return cl.size, nil
}

type mountedLayer struct {
	tarStreamer
	mountID string
	parent  *cacheLayer
	path    string
}

func (ml *mountedLayer) TarStream() (io.Reader, error) {
	return ml.tarStreamer()
}

func (ml *mountedLayer) Path() (string, error) {
	return ml.path, nil
}

func (ml *mountedLayer) Parent() (Layer, error) {
	return ml.parent, nil
}

type layerStore struct {
	root   string
	driver graphdriver.Driver

	layerMap map[LayerAddress]*cacheLayer
	layerL   sync.Mutex

	mounts map[string]*mountedLayer
	mountL sync.Mutex
}

func NewLayerStore(root string, driver graphdriver.Driver) (LayerStore, error) {
	ls := &layerStore{
		root:     root,
		driver:   driver,
		layerMap: map[LayerAddress]*cacheLayer{},
		mounts:   map[string]*mountedLayer{},
	}

	// TODO: Load existing layers and references

	return ls, nil
}

func (ls *layerStore) Register(ts io.Reader, parent LayerAddress) (Layer, error) {
	var pid string
	var p *cacheLayer
	if string(parent) != "" {
		l, ok := ls.layerMap[parent]
		if !ok {
			return nil, ErrLayerDoesNotExist
		}
		p = l
		pid = l.cacheID
	}

	// Create new cacheLayer
	layer := &cacheLayer{
		parent:  p,
		cacheID: stringid.GenerateRandomID(),
	}

	if err := ls.driver.Create(layer.cacheID, pid); err != nil {
		return nil, err
	}

	var err error
	defer func() {
		if err != nil {
			logrus.Debugf("Cleaning up layer %s: %v", layer.cacheID, err)
			if err := ls.driver.Remove(layer.cacheID); err != nil {
				logrus.Errorf("Error cleaning up cache layer %s: %v", layer.cacheID, err)
			}
		}
	}()

	digester := digest.Canonical.New()
	tr := io.TeeReader(ts, digester.Hash())

	layer.size, err = ls.driver.ApplyDiff(layer.cacheID, pid, archive.Reader(tr))
	if err != nil {
		return nil, err
	}

	layer.tarStreamer = func() (io.Reader, error) {
		archiver, err := ls.driver.Diff(layer.cacheID, pid)
		return io.Reader(archiver), err
	}

	var layerHash digest.Digest
	if layer.parent == nil {
		layerHash = digester.Digest()
	} else {
		layerHashParts := digester.Digest().String() + " " + digest.Digest(layer.parent.address).String()
		layerHash, err = digest.FromBytes([]byte(layerHashParts))
		if err != nil {
			return nil, err
		}
	}
	layer.address = LayerAddress(layerHash)

	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	if existingLayer, ok := ls.layerMap[layer.address]; ok {
		// Set error for cleanup, but do not return
		err = errors.New("layer already exists")
		return existingLayer, nil
	}

	ls.layerMap[layer.address] = layer

	// TODO: Persist mapping update to disk

	return layer, nil
}

func (ls *layerStore) Get(l LayerAddress) (Layer, error) {
	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	layer, ok := ls.layerMap[l]
	if !ok {
		return nil, ErrLayerDoesNotExist
	}
	// TODO: Add name here to keep reference?
	return layer, nil
}

func (ls *layerStore) Put(l LayerAddress) error {
	// TODO: Release reference, attempt garbage collections
	// NOTE: If put is called on layer before layers have all
	// been fully retrieved via Get, layers may unintentionally
	// be removed.
	return nil
}

func (ls *layerStore) Mount(id string, parent LayerAddress) (RWLayer, error) {
	ls.mountL.Lock()
	defer ls.mountL.Unlock()
	if m, ok := ls.mounts[id]; ok {
		return m, nil
	}

	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	var pid string
	var p *cacheLayer
	if string(parent) != "" {
		l, ok := ls.layerMap[parent]
		if !ok {
			return nil, ErrLayerDoesNotExist
		}
		p = l
		pid = l.cacheID
	}

	mount := &mountedLayer{
		parent:  p,
		mountID: stringid.GenerateRandomID(),
	}

	if err := ls.driver.Create(mount.mountID, pid); err != nil {
		return nil, err
	}

	mount.tarStreamer = func() (io.Reader, error) {
		archiver, err := ls.driver.Diff(mount.mountID, pid)
		return io.Reader(archiver), err
	}

	dir, err := ls.driver.Get(mount.mountID, "")
	if err != nil {
		// TODO: Cleaup
		return nil, err
	}
	mount.path = dir

	ls.mounts[id] = mount

	// TODO: Persist mapping update to disk

	return mount, nil
}

func (ls *layerStore) Unmount(id string) error {
	ls.mountL.Lock()
	defer ls.mountL.Unlock()

	m := ls.mounts[id]
	if m == nil {
		return errors.New("mount does not exist")
	}

	delete(ls.mounts, id)

	return ls.driver.Remove(m.mountID)
}
