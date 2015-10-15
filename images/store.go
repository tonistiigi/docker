package images

import (
	"encoding/json"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
)

// Store is an interface for creating and accessing images
type Store interface {
	Create(config []byte) (ID, error)
	Get(id ID) (*Image, error)
	Search(string) (ID, error)
	SetParent(ID, ID) error
	GetParent(ID) (ID, error)
}

type store struct {
	sync.Mutex
	ls        layer.Store
	ids       map[ID]layer.Layer
	fs        StoreBackend
	digestSet *digest.Set
}

// NewImageStore returns new store object for given layer.Store
func NewImageStore(fs StoreBackend, ls layer.Store) (Store, error) {
	is := &store{
		ls:        ls,
		ids:       make(map[ID]layer.Layer),
		fs:        fs,
		digestSet: digest.NewSet(),
	}

	// load all current images and retain layers
	if err := is.restore(); err != nil {
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
		layerID, err := layer.CreateID("", img.RootFS.DiffIDs...)
		if err != nil {
			return err
		}
		layer, err := is.ls.Get(layerID)
		if err != nil {
			return err
		}
		is.ids[ID(id)] = layer
		if err := is.digestSet.Add(id); err != nil {
			delete(is.ids, ID(id))
			return err
		}

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

	layerID, err := layer.CreateID("", img.RootFS.DiffIDs...)
	if err != nil {
		return "", err
	}
	layer, err := is.ls.Get(layerID)
	if err != nil {
		return "", err
	}

	is.Lock()
	defer is.Unlock()
	is.ids[imageID] = layer
	if err := is.digestSet.Add(digest.Digest(imageID)); err != nil {
		delete(is.ids, imageID)
		return "", err
	}

	return imageID, nil
}

func (is *store) Search(term string) (ID, error) {
	dgst, err := is.digestSet.Lookup(term)
	if err != nil {
		return "", err
	}
	return ID(dgst), nil
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
	img.rawJSON = config

	return &img, nil
}

func (is *store) SetParent(id, parent ID) error {
	return is.fs.SetMetadata(digest.Digest(id), "parent", []byte(parent))
}

func (is *store) GetParent(id ID) (ID, error) {
	d, err := is.fs.GetMetadata(digest.Digest(id), "parent")
	if err != nil {
		return "", err
	}
	return ID(d), nil // todo: validate?
}
