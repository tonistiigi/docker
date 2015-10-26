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
)

func (ls *layerStore) MountByGraphID(name string, graphID string, parent ID) (RWLayer, error) {
	var err error

	ls.mountL.Lock()
	defer ls.mountL.Unlock()
	m, ok := ls.mounts[name]
	if ok {
		if m.parent.address != parent {
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

	var p *cacheLayer
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

func (ls *layerStore) RegisterByGraphID(graphID string, parent ID, tarDataFile string) (Layer, error) {
	var err error
	var p *cacheLayer
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

	// Create new cacheLayer
	layer := &cacheLayer{
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

	if tarDataFile != "" {
		var (
			tdf          *os.File
			tsw          io.WriteCloser
			uncompressed io.ReadCloser
			ar           io.Reader
		)

		tsw, err = tx.TarSplitWriter()
		if err != nil {
			return nil, err
		}

		defer tsw.Close()
		tdf, err = os.Open(tarDataFile)
		if err != nil {
			return nil, err
		}
		defer tdf.Close()

		uncompressed, err = gzip.NewReader(tdf)
		if err != nil {
			return nil, err
		}
		defer uncompressed.Close()

		tr := io.TeeReader(uncompressed, tsw)
		trc := ioutils.NewReadCloserWrapper(tr, uncompressed.Close)

		ar, err = ls.assembleTar(graphID, trc)
		if err != nil {
			return nil, err
		}
		digester := digest.Canonical.New()
		_, err = io.Copy(digester.Hash(), ar)
		if err != nil {
			return nil, err
		}

		layer.digest = DiffID(digester.Digest())
	} else {
		// TODO Create tar stream
		// Write both
		return nil, errors.New("migrating without tar split data not supported")
	}

	layer.address = createIDFromParent(parent, layer.digest)

	if err = storeLayer(tx, layer); err != nil {
		return nil, err
	}

	ls.layerL.Lock()
	defer ls.layerL.Unlock()

	if existingLayer := ls.getAndRetainLayer(layer.address); existingLayer != nil {
		// Set error for cleanup, but do not return
		err = errors.New("layer already exists")
		return existingLayer.getReference(), nil
	}

	if err = tx.Commit(layer.address); err != nil {
		return nil, err
	}

	ls.layerMap[layer.address] = layer

	return layer.getReference(), nil
}
