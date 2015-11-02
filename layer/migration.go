package layer

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
)

func (ls *layerStore) MountByGraphID(name string, graphID string, parent ChainID) (l RWLayer, err error) {
	ls.mountL.Lock()
	defer ls.mountL.Unlock()
	m, ok := ls.mounts[name]
	if ok {
		if m.parent.chainID != parent {
			return nil, errors.New("name conflict, mismatched parent")
		}
		if m.mountID != graphID {
			return nil, errors.New("mount already exists")
		}

		return m, nil
	}

	if !ls.driver.Exists(graphID) {
		return nil, errors.New("graph ID does not exist")
	}

	var p *roLayer
	if string(parent) != "" {
		ls.layerL.Lock()
		p = ls.getAndRetainLayer(parent)
		ls.layerL.Unlock()
		if p == nil {
			return nil, ErrLayerDoesNotExist
		}

		// Release parent chain if error
		defer func() {
			if err != nil {
				ls.layerL.Lock()
				ls.releaseLayer(p)
				ls.layerL.Unlock()
			}
		}()
	}

	// TODO: Ensure graphID has correct parent

	m = &mountedLayer{
		name:       name,
		parent:     p,
		mountID:    graphID,
		layerStore: ls,
	}

	// Check for existing init layer
	initID := fmt.Sprintf("%s-init", graphID)
	if ls.driver.Exists(initID) {
		m.initID = initID
	}

	if err = ls.saveMount(m); err != nil {
		return nil, err
	}

	// TODO: provide a mount label
	if err = ls.mount(m, ""); err != nil {
		return nil, err
	}

	return m, nil
}

func (ls *layerStore) migrateLayer(tx MetadataTransaction, tarDataFile string, layer *roLayer) error {
	var ar io.Reader
	if tarDataFile != "" {
		tsw, err := tx.TarSplitWriter()
		if err != nil {
			return err
		}

		defer tsw.Close()
		tdf, err := os.Open(tarDataFile)
		if err != nil {
			return err
		}
		defer tdf.Close()

		uncompressed, err := gzip.NewReader(tdf)
		if err != nil {
			return err
		}
		defer uncompressed.Close()

		tr := io.TeeReader(uncompressed, tsw)
		trc := ioutils.NewReadCloserWrapper(tr, uncompressed.Close)

		ar, err = ls.assembleTar(layer.cacheID, trc)
		if err != nil {
			return err
		}

	} else {
		var graphParent string
		if layer.parent != nil {
			graphParent = layer.parent.cacheID
		}
		archiver, err := ls.driver.Diff(layer.cacheID, graphParent)
		if err != nil {
			return err
		}
		defer archiver.Close()

		tsw, err := tx.TarSplitWriter()
		if err != nil {
			return err
		}
		metaPacker := storage.NewJSONPacker(tsw)
		defer tsw.Close()

		ar, err = asm.NewInputTarStream(archiver, metaPacker, nil)
		if err != nil {
			return err
		}
	}

	digester := digest.Canonical.New()
	n, err := io.Copy(digester.Hash(), ar)
	if err != nil {
		return err
	}

	layer.size = n
	layer.diffID = DiffID(digester.Digest())

	return nil
}

func (ls *layerStore) RegisterByGraphID(graphID string, parent ChainID, tarDataFile string) (Layer, error) {
	// err is used to hold the error which will always trigger
	// cleanup of creates sources but may not be an error returned
	// to the caller (already exists).
	var err error
	var p *roLayer
	if string(parent) != "" {
		p = ls.get(parent)
		if p == nil {
			return nil, ErrLayerDoesNotExist
		}

		// Release parent chain if error
		defer func() {
			if err != nil {
				ls.layerL.Lock()
				ls.releaseLayer(p)
				ls.layerL.Unlock()
			}
		}()
	}

	// Create new roLayer
	layer := &roLayer{
		parent:         p,
		cacheID:        graphID,
		referenceCount: 1,
		layerStore:     ls,
		references:     map[Layer]struct{}{},
	}

	tx, err := ls.store.StartTransaction()
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			logrus.Debugf("Cleaning up transaction after failed migration for %s: %v", graphID, err)
			if err := tx.Cancel(); err != nil {
				logrus.Errorf("Error canceling metadata transaction %q: %s", tx.String(), err)
			}
		}
	}()

	if err = ls.migrateLayer(tx, tarDataFile, layer); err != nil {
		return nil, err
	}

	layer.chainID = createChainIDFromParent(parent, layer.diffID)

	if err = storeLayer(tx, layer); err != nil {
		return nil, err
	}

	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	if existingLayer := ls.getAndRetainLayer(layer.chainID); existingLayer != nil {
		// Set error for cleanup, but do not return
		err = errors.New("layer already exists")
		return existingLayer.getReference(), nil
	}

	if err = tx.Commit(layer.chainID); err != nil {
		return nil, err
	}

	ls.layerMap[layer.chainID] = layer

	return layer.getReference(), nil
}
