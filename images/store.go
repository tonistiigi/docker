package images

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layer"
)

// Store is an interface for creating and accessing images
type Store interface {
	Create(config []byte) (ID, error)
	Get(id ID) (*Image, error)
	Delete(id ID) ([]layer.Metadata, error)
	Search(partialID string) (ID, error)
	SetParent(id ID, parent ID) error
	GetParent(id ID) (ID, error)
	HasChild(id ID) bool
}

type store struct {
	sync.Mutex
	ls        layer.Store
	ids       map[ID]layer.Layer
	children  map[ID]map[ID]struct{}
	fs        StoreBackend
	digestSet *digest.Set
}

// NewImageStore returns new store object for given layer.Store
func NewImageStore(fs StoreBackend, ls layer.Store) (Store, error) {
	is := &store{
		ls:        ls,
		ids:       make(map[ID]layer.Layer),
		children:  make(map[ID]map[ID]struct{}),
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

		if parent, err := is.GetParent(ID(id)); err == nil {
			if is.children[parent] == nil {
				is.children[parent] = make(map[ID]struct{})
			}
			is.children[parent][ID(id)] = struct{}{}
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

	is.Lock()
	defer is.Unlock()

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

	is.ids[imageID] = layer
	if err := is.digestSet.Add(digest.Digest(imageID)); err != nil {
		delete(is.ids, imageID)
		return "", err
	}

	return imageID, nil
}

func (is *store) Search(term string) (ID, error) {
	is.Lock()
	defer is.Unlock()

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

func (is *store) Delete(id ID) ([]layer.Metadata, error) {
	is.Lock()
	defer is.Unlock()

	if parent, err := is.GetParent(id); err == nil && is.children[parent] != nil {
		delete(is.children[parent], id)
		if len(is.children[parent]) == 0 {
			delete(is.children, parent)
		}
	}

	l, present := is.ids[id]
	if !present {
		return nil, fmt.Errorf("unrecognized image ID %s", id.String())
	}
	delete(is.ids, id)
	is.fs.Delete(digest.Digest(id))

	return is.ls.Release(l)
}

func (is *store) SetParent(id, parent ID) error {
	is.Lock()
	if is.children[parent] == nil {
		is.children[parent] = make(map[ID]struct{})
	}
	is.children[parent][id] = struct{}{}
	is.Unlock()
	return is.fs.SetMetadata(digest.Digest(id), "parent", []byte(parent))
}

func (is *store) GetParent(id ID) (ID, error) {
	d, err := is.fs.GetMetadata(digest.Digest(id), "parent")
	if err != nil {
		return "", err
	}
	return ID(d), nil // todo: validate?
}

func (is *store) HasChild(id ID) bool {
	is.Lock()
	defer is.Unlock()
	return len(is.children[id]) > 0
}
