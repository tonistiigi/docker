package images

import (
	"encoding/json"
	"path/filepath"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layers"
)

// Store is an interface for creating and accessing images
type Store interface {
	Create(config []byte) (ID, error)
	Get(id ID) (*Image, error)
}

type store struct {
	sync.Mutex
	ls   layers.LayerStore
	root string
	ids  map[ID]layers.Layer
	fs   StoreBackend
}

const imagesDirName = "images"

// NewImageStore returns new store object for given LayerStore
func NewImageStore(root string, ls layers.LayerStore) (Store, error) {
	fs, err := newFSStore(filepath.Join(root, imagesDirName))
	if err != nil {
		return nil, err
	}

	is := &store{
		root: root,
		ls:   ls,
		ids:  make(map[ID]layers.Layer),
		fs:   fs,
	}

	// load all current images and retain layers
	if err := is.restore(); err != nil {
		return nil, err
	}

	if err := is.migrateV1Images(); err != nil {
		return nil, err
	}

	return is, nil
}

func (is *store) restore() error {
	return is.fs.Walk(func(id digest.Digest) error {
		img, err := is.Get(ID(id))
		if err != nil {
			logrus.Errorf("invalid image %v, %v", id, err)
			return nil
		}
		layerID, err := layers.LayerID("", img.DiffIDs...)
		if err != nil {
			return err
		}
		layer, err := is.ls.Get(layerID)
		if err != nil {
			return err
		}
		is.ids[ID(id)] = layer

		return nil
	})
}

func (is *store) Create(config []byte) (ID, error) {
	// strongID
	// store into file
	// remove all if something failed
	var img Image
	err := json.Unmarshal(config, &img)
	if err != nil {
		return "", err
	}

	dgst, err := is.fs.Set(config)
	if err != nil {
		return "", err
	}
	imageID := ID(dgst)

	if _, exists := is.ids[imageID]; exists {
		return imageID, nil
	}

	layerID, err := layers.LayerID("", img.DiffIDs...)
	if err != nil {
		return "", err
	}
	layer, err := is.ls.Get(layerID)
	if err != nil {
		return "", err
	}

	is.ids[imageID] = layer

	return imageID, nil
}

func (is *store) Get(id ID) (*Image, error) {
	// todo: validate digest(maybe imageID)
	// check if image is in ids, detect manual insertions and start using them
	config, err := is.fs.Get(digest.Digest(id))
	if err != nil {
		return nil, err
	}

	var img Image
	err = json.Unmarshal(config, &img)
	if err != nil {
		return nil, err
	}

	img.ID = id
	// todo: load parent file

	return &img, nil
}
