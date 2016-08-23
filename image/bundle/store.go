package bundle

import (
	"fmt"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
)

// Store is an interface for creating and accessing images
type Store interface {
	Create(config []byte) (ID, error)
	Get(id ID) (*Bundle, error)
	Delete(id ID) ([]layer.Metadata, error)
	Search(partialID string) (ID, error)
	Map() map[ID]*Bundle
}

// LayerGetReleaser is a minimal interface for getting and releasing images.
type LayerGetReleaser interface {
	Get(layer.ChainID) (layer.Layer, error)
	Release(layer.Layer) ([]layer.Metadata, error)
}

type imageMeta struct {
	layer    layer.Layer
	children map[ID]struct{}
}

type store struct {
	mu        sync.RWMutex
	is        image.Store
	bundles   map[ID]struct{}
	fs        image.StoreBackend
	digestSet *digest.Set
}

// NewBundleStore returns new store object for given backend and image store.
func NewBundleStore(fs image.StoreBackend, is image.Store) (Store, error) {
	bs := &store{
		is:        is,
		bundles:   make(map[ID]struct{}),
		fs:        fs,
		digestSet: digest.NewSet(),
	}

	// load all current images and retain layers
	if err := bs.restore(); err != nil {
		return nil, err
	}

	return bs, nil
}

func (bs *store) restore() error {
	err := bs.fs.Walk(func(dgst digest.Digest) error {
		bundle, err := bs.Get(IDFromDigest(dgst))
		if err != nil {
			logrus.Errorf("invalid bundle %v, %v", dgst, err)
			return nil
		}
		for _, s := range bundle.Services {
			if _, err := bs.is.Get(s.Image); err != nil {
				return err
			}
		}
		if err := bs.digestSet.Add(dgst); err != nil {
			return err
		}
		bs.bundles[IDFromDigest(dgst)] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (bs *store) Create(config []byte) (ID, error) {
	bundle, err := NewFromJSON(config)
	if err != nil {
		return "", err
	}
	for _, s := range bundle.Services {
		if s.Name == "" {
			return "", fmt.Errorf("empty service name not allowed")
		}
	}

	dgst, err := bs.fs.Set(config)
	if err != nil {
		return "", err
	}
	bundleID := IDFromDigest(dgst)

	bs.mu.Lock()
	defer bs.mu.Unlock()

	if _, exists := bs.bundles[bundleID]; exists {
		return bundleID, nil
	}

	bs.bundles[bundleID] = struct{}{}
	if err := bs.digestSet.Add(bundleID.Digest()); err != nil {
		delete(bs.bundles, bundleID)
		return "", err
	}

	return bundleID, nil
}

func (bs *store) Search(term string) (ID, error) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	dgst, err := bs.digestSet.Lookup(term)
	if err != nil {
		if err == digest.ErrDigestNotFound {
			err = fmt.Errorf("No such bundle: %s", term)
		}
		return "", err
	}
	return IDFromDigest(dgst), nil
}

func (bs *store) Get(id ID) (*Bundle, error) {
	// todo: Check if bundle is in bundles
	// todo: Detect manual insertions and start using them
	config, err := bs.fs.Get(id.Digest())
	if err != nil {
		return nil, err
	}

	bundle, err := NewFromJSON(config)
	if err != nil {
		return nil, err
	}
	bundle.computedID = id

	return bundle, nil
}

func (bs *store) Delete(id ID) ([]layer.Metadata, error) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if err := bs.digestSet.Remove(id.Digest()); err != nil {
		logrus.Errorf("error removing %s from digest set: %q", id, err)
	}
	delete(bs.bundles, id)
	bs.fs.Delete(id.Digest())

	// todo: unreference images

	return nil, nil
}

func (bs *store) Map() map[ID]*Bundle {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	bundles := make(map[ID]*Bundle)

	for id := range bs.bundles {
		bundle, err := bs.Get(id)
		if err != nil {
			logrus.Errorf("invalid bundle access: %q, error: %q", id, err)
			continue
		}
		bundles[id] = bundle
	}
	return bundles
}
