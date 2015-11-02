package layer

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stringid"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
)

type layerStore struct {
	store  MetadataStore
	driver graphdriver.Driver

	layerMap map[ID]*cacheLayer
	layerL   sync.Mutex

	mounts map[string]*mountedLayer
	mountL sync.Mutex
}

// NewStore creates a new Store instance using
// the provided metadata store and graph driver.
// The metadata store will be used to restore
// the Store.
func NewStore(store MetadataStore, driver graphdriver.Driver) (Store, error) {
	ls := &layerStore{
		store:    store,
		driver:   driver,
		layerMap: map[ID]*cacheLayer{},
		mounts:   map[string]*mountedLayer{},
	}

	ids, mounts, err := store.List()
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		l, err := ls.loadLayer(id)
		if err != nil {
			logrus.Debugf("Failed to load layer %s: %s", id, err)
		}
		if l.parent != nil {
			l.parent.referenceCount++
		}
	}

	for _, mount := range mounts {
		if err := ls.loadMount(mount); err != nil {
			logrus.Debugf("Failed to load mount %s: %s", mount, err)
		}
	}

	return ls, nil
}

func (ls *layerStore) loadLayer(layer ID) (*cacheLayer, error) {
	cl, ok := ls.layerMap[layer]
	if ok {
		return cl, nil
	}

	diff, err := ls.store.GetDiffID(layer)
	if err != nil {
		return nil, err
	}

	size, err := ls.store.GetSize(layer)
	if err != nil {
		return nil, err
	}

	cacheID, err := ls.store.GetCacheID(layer)
	if err != nil {
		return nil, err
	}

	parent, err := ls.store.GetParent(layer)
	if err != nil {
		return nil, err
	}

	cl = &cacheLayer{
		address:    layer,
		digest:     diff,
		size:       size,
		cacheID:    cacheID,
		layerStore: ls,
		references: map[Layer]struct{}{},
	}

	if parent != "" {
		p, err := ls.loadLayer(parent)
		if err != nil {
			return nil, err
		}
		cl.parent = p
	}

	ls.layerMap[cl.address] = cl

	return cl, nil
}

func (ls *layerStore) loadMount(mount string) error {
	if _, ok := ls.mounts[mount]; ok {
		return nil
	}

	mountID, err := ls.store.GetMountID(mount)
	if err != nil {
		return err
	}

	initID, err := ls.store.GetInitID(mount)
	if err != nil {
		return err
	}

	parent, err := ls.store.GetMountParent(mount)
	if err != nil {
		return err
	}

	ml := &mountedLayer{
		name:       mount,
		mountID:    mountID,
		initID:     initID,
		layerStore: ls,
	}

	if parent != "" {
		p, err := ls.loadLayer(parent)
		if err != nil {
			return err
		}
		ml.parent = p

		p.referenceCount++
	}

	ls.mounts[ml.name] = ml

	return nil
}

func (ls *layerStore) applyTar(tx MetadataTransaction, ts io.Reader, parent string, layer *cacheLayer) error {
	digester := digest.Canonical.New()
	tr := io.TeeReader(ts, digester.Hash())

	tsw, err := tx.TarSplitWriter()
	if err != nil {
		return err
	}
	metaPacker := storage.NewJSONPacker(tsw)
	defer tsw.Close()

	// we're passing nil here for the file putter, because the ApplyDiff will
	// handle the extraction of the archive
	rdr, err := asm.NewInputTarStream(tr, metaPacker, nil)
	if err != nil {
		return err
	}

	rc := &readCounter{
		r: rdr,
	}

	applySize, err := ls.driver.ApplyDiff(layer.cacheID, parent, archive.Reader(rc))
	if err != nil {
		return err
	}

	// Discard trailing data but ensure metadata is picked up to reconstruct stream
	if _, err := io.Copy(ioutil.Discard, rc); err != nil {
		return err
	}

	layer.size = rc.count
	layer.digest = DiffID(digester.Digest())

	logrus.Debugf("Applied tar %s to %s, size: %d", layer.digest, layer.cacheID, applySize)

	return nil
}

type readCounter struct {
	r     io.Reader
	count int64
}

func (rc *readCounter) Read(p []byte) (n int, err error) {
	n, err = rc.r.Read(p)
	rc.count += int64(n)
	return
}

func (ls *layerStore) Register(ts io.Reader, parent ID) (Layer, error) {
	var err error
	var pid string
	var p *cacheLayer
	if string(parent) != "" {
		p = ls.get(parent)
		if p == nil {
			return nil, ErrLayerDoesNotExist
		}
		pid = p.cacheID
		// Release parent chain if error
		defer func() {
			if err != nil {
				ls.layerL.Lock()
				ls.releaseLayer(p)
				ls.layerL.Unlock()
			}
		}()
	}

	// Create new cacheLayer
	layer := &cacheLayer{
		parent:         p,
		cacheID:        stringid.GenerateRandomID(),
		referenceCount: 1,
		layerStore:     ls,
		references:     map[Layer]struct{}{},
	}

	if err = ls.driver.Create(layer.cacheID, pid); err != nil {
		return nil, err
	}

	tx, err := ls.store.StartTransaction()
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			logrus.Debugf("Cleaning up layer %s: %v", layer.cacheID, err)
			if err := ls.driver.Remove(layer.cacheID); err != nil {
				logrus.Errorf("Error cleaning up cache layer %s: %v", layer.cacheID, err)
			}
			if err := tx.Cancel(); err != nil {
				logrus.Errorf("Error canceling metadata transaction %q: %s", tx.String(), err)
			}
		}
	}()

	if err = ls.applyTar(tx, ts, pid, layer); err != nil {
		return nil, err
	}

	if layer.parent == nil {
		layer.address = ID(layer.digest)
	} else {
		layer.address = createIDFromParent(layer.parent.address, layer.digest)
	}

	if err = storeLayer(tx, layer); err != nil {
		return nil, err
	}

	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	if existingLayer := ls.getAndRetainLayer(layer.address); existingLayer != nil {
		// Set error for cleanup, but do not return the error
		err = errors.New("layer already exists")
		return existingLayer.getReference(), nil
	}

	if err = tx.Commit(layer.address); err != nil {
		return nil, err
	}

	ls.layerMap[layer.address] = layer

	return layer.getReference(), nil
}

func (ls *layerStore) get(l ID) *cacheLayer {
	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	layer, ok := ls.layerMap[l]
	if !ok {
		return nil
	}

	layer.referenceCount++

	return layer
}

func (ls *layerStore) Get(l ID) (Layer, error) {
	layer := ls.get(l)
	if layer == nil {
		return nil, ErrLayerDoesNotExist
	}

	return layer.getReference(), nil
}

func (ls *layerStore) deleteLayer(layer *cacheLayer, metadata *Metadata) error {
	err := ls.driver.Remove(layer.cacheID)
	if err != nil {
		return err
	}

	err = ls.store.Remove(layer.address)
	if err != nil {
		return err
	}
	metadata.DiffID = layer.digest
	metadata.LayerID = layer.address
	metadata.Size, err = layer.Size()
	if err != nil {
		return err
	}
	metadata.DiffSize = layer.size

	return nil
}

func (ls *layerStore) releaseLayers(l *cacheLayer, removed *[]Metadata, depth int) error {
	if l.referenceCount == 0 {
		panic("layer not retained")
	}
	l.referenceCount--
	if l.referenceCount == 0 {
		if len(*removed) == 0 && depth > 0 {
			panic("cannot remove layer with child")
		}
		if l.hasReferences() {
			panic("cannot delete referenced layer")
		}
		var metadata Metadata
		if err := ls.deleteLayer(l, &metadata); err != nil {
			return err
		}

		delete(ls.layerMap, l.address)
		*removed = append(*removed, metadata)

		if l.parent != nil {
			if err := ls.releaseLayers(l.parent, removed, depth+1); err != nil {
				return err
			}
		}
	}

	return nil
}

func (ls *layerStore) releaseLayer(layer *cacheLayer) ([]Metadata, error) {
	removed := []Metadata{}
	err := ls.releaseLayers(layer, &removed, 0)
	return removed, err
}

func (ls *layerStore) Release(l Layer) ([]Metadata, error) {
	ls.layerL.Lock()
	defer ls.layerL.Unlock()
	layer, ok := ls.layerMap[l.ID()]
	if !ok {
		return []Metadata{}, nil
	}
	if !layer.hasReference(l) {
		return nil, ErrLayerNotRetained
	}

	layer.deleteReference(l)

	return ls.releaseLayer(layer)
}

func (ls *layerStore) mount(m *mountedLayer, mountLabel string) error {
	dir, err := ls.driver.Get(m.mountID, mountLabel)
	if err != nil {
		return err
	}
	m.path = dir
	m.activityCount++

	return nil
}

func (ls *layerStore) saveMount(mount *mountedLayer) error {
	if err := ls.store.SetMountID(mount.name, mount.mountID); err != nil {
		return err
	}

	if mount.initID != "" {
		if err := ls.store.SetInitID(mount.name, mount.initID); err != nil {
			return err
		}
	}

	if mount.parent != nil {
		if err := ls.store.SetMountParent(mount.name, mount.parent.address); err != nil {
			return err
		}
	}

	ls.mounts[mount.name] = mount

	return nil
}

func (ls *layerStore) getAndRetainLayer(layer ID) *cacheLayer {
	l, ok := ls.layerMap[layer]
	if !ok {
		return nil
	}

	l.referenceCount++

	return l
}

func (ls *layerStore) initMount(graphID, parent string, initFunc MountInit) (string, error) {
	// Use "<graph-id>-init" to maintain compatibility with graph drivers
	// which are expecting this layer with this special name. If all
	// graph drivers can be updated to not rely on knowin about this layer
	// then the initID should be randomly generated.
	initID := fmt.Sprintf("%s-init", graphID)

	if err := ls.driver.Create(initID, parent); err != nil {

	}
	p, err := ls.driver.Get(initID, "")
	if err != nil {
		return "", err
	}

	if err := initFunc(p); err != nil {
		ls.driver.Put(initID)
		return "", err
	}

	if err := ls.driver.Put(initID); err != nil {
		return "", err
	}

	return initID, nil
}

func (ls *layerStore) Mount(name string, parent ID, mountLabel string, initFunc MountInit) (RWLayer, error) {
	var err error
	ls.mountL.Lock()
	defer ls.mountL.Unlock()
	m, ok := ls.mounts[name]
	if ok {
		// Check if has path
		if err := ls.mount(m, mountLabel); err != nil {
			return nil, err
		}
		return m, nil
	}

	var pid string
	var p *cacheLayer
	if string(parent) != "" {
		ls.layerL.Lock()
		p = ls.getAndRetainLayer(parent)
		ls.layerL.Unlock()
		if p == nil {
			return nil, ErrLayerDoesNotExist
		}
		pid = p.cacheID

		// Release parent chain if error
		defer func() {
			if err != nil {
				ls.layerL.Lock()
				ls.releaseLayer(p)
				ls.layerL.Unlock()
			}
		}()
	}

	m = &mountedLayer{
		name:       name,
		parent:     p,
		mountID:    stringid.GenerateRandomID(),
		layerStore: ls,
	}

	if initFunc != nil {
		pid, err = ls.initMount(m.mountID, pid, initFunc)
		if err != nil {
			return nil, err
		}
		m.initID = pid
	}

	if err = ls.driver.Create(m.mountID, pid); err != nil {
		return nil, err
	}

	if err = ls.saveMount(m); err != nil {
		return nil, err
	}

	if err = ls.mount(m, mountLabel); err != nil {
		return nil, err
	}

	return m, nil
}

func (ls *layerStore) Unmount(name string) error {
	ls.mountL.Lock()
	defer ls.mountL.Unlock()

	m := ls.mounts[name]
	if m == nil {
		return ErrMountDoesNotExist
	}

	m.activityCount--

	if err := ls.driver.Put(m.mountID); err != nil {
		return err
	}

	return nil
}

func (ls *layerStore) DeleteMount(name string) ([]Metadata, error) {
	ls.mountL.Lock()
	defer ls.mountL.Unlock()

	m := ls.mounts[name]
	if m == nil {
		return nil, ErrMountDoesNotExist
	}
	if m.activityCount > 0 {
		return nil, ErrActiveMount
	}

	delete(ls.mounts, name)

	if err := ls.driver.Remove(m.mountID); err != nil {
		logrus.Errorf("Error removing mounted layer %s: %s", m.name, err)
		return nil, err
	}

	if m.initID != "" {
		if err := ls.driver.Remove(m.initID); err != nil {
			logrus.Errorf("Error removing init layer %s: %s", m.name, err)
			return nil, err
		}
	}

	if err := ls.store.RemoveMount(m.name); err != nil {
		logrus.Errorf("Error removing mount metadata: %s: %s", m.name, err)
		return nil, err
	}

	ls.layerL.Lock()
	defer ls.layerL.Unlock()
	if m.parent != nil {
		return ls.releaseLayer(m.parent)
	}

	return []Metadata{}, nil
}

func (ls *layerStore) Changes(name string) ([]archive.Change, error) {
	ls.mountL.Lock()
	m := ls.mounts[name]
	ls.mountL.Unlock()
	if m == nil {
		return nil, ErrMountDoesNotExist
	}
	pid := m.initID
	if pid == "" && m.parent != nil {
		pid = m.parent.cacheID
	}
	return ls.driver.Changes(m.mountID, pid)
}

func (ls *layerStore) assembleTar(graphID string, metadata io.ReadCloser) (io.Reader, error) {
	// get our relative path to the container
	fsPath, err := ls.driver.Get(graphID, "")
	if err != nil {
		metadata.Close()
		return nil, err
	}

	pR, pW := io.Pipe()
	// this will need to be in a goroutine, as we are returning the stream of a
	// tar archive, but can not close the metadata reader early (when this
	// function returns)...
	go func() {
		defer ls.driver.Put(graphID)
		defer metadata.Close()

		metaUnpacker := storage.NewJSONUnpacker(metadata)
		fileGetter := storage.NewPathFileGetter(fsPath)
		logrus.Debugf("Assembling tar data for %s from %s", graphID, fsPath)
		ots := asm.NewOutputTarStream(fileGetter, metaUnpacker)
		defer ots.Close()
		if _, err := io.Copy(pW, ots); err != nil {
			pW.CloseWithError(err)
			return
		}
		pW.Close()
	}()
	return pR, nil
}
