package image

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
	Children(id ID) []ID
	Map() map[ID]*Image
	Heads() map[ID]*Image
}

type imageMeta struct {
	layer    layer.Layer
	children map[ID]struct{}
}

type store struct {
	sync.Mutex
	ls        layer.Store
	images    map[ID]*imageMeta
	fs        StoreBackend
	digestSet *digest.Set
}

// NewImageStore returns new store object for given layer.Store
func NewImageStore(fs StoreBackend, ls layer.Store) (Store, error) {
	is := &store{
		ls:        ls,
		images:    make(map[ID]*imageMeta),
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
	err := is.fs.Walk(func(id digest.Digest) error {
		img, err := is.Get(ID(id))
		if err != nil {
			logrus.Errorf("invalid image %v, %v", id, err)
			return nil
		}
		var l layer.Layer
		if len(img.RootFS.DiffIDs) > 0 {
			l, err = is.ls.Get(layer.CreateID(img.RootFS.DiffIDs))
			if err != nil {
				return err
			}
		}
		if err := is.digestSet.Add(id); err != nil {
			return err
		}

		imageMeta := &imageMeta{
			layer:    l,
			children: make(map[ID]struct{}),
		}

		is.images[ID(id)] = imageMeta

		return nil
	})
	if err != nil {
		return err
	}

	// Second pass to fill in children maps
	for id := range is.images {
		if parent, err := is.GetParent(id); err == nil {
			if parentMeta := is.images[parent]; parentMeta != nil {
				parentMeta.children[id] = struct{}{}
			}
		}
	}

	return nil
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

	if _, exists := is.images[imageID]; exists {
		return imageID, nil
	}

	layerID := layer.CreateID(img.RootFS.DiffIDs)

	var l layer.Layer
	if layerID != "" {
		l, err = is.ls.Get(layerID)
		if err != nil {
			return "", err
		}
	}

	imageMeta := &imageMeta{
		layer:    l,
		children: make(map[ID]struct{}),
	}

	is.images[imageID] = imageMeta
	if err := is.digestSet.Add(digest.Digest(imageID)); err != nil {
		delete(is.images, imageID)
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
	// check if image is in images, detect manual insertions and start using them
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

	img.Parent, err = is.GetParent(id)
	if err != nil {
		img.Parent = ""
	}

	return &img, nil
}

func (is *store) Delete(id ID) ([]layer.Metadata, error) {
	is.Lock()
	defer is.Unlock()

	if parent, err := is.GetParent(id); err == nil && is.images[parent] != nil {
		delete(is.images[parent].children, id)
	}

	imageMeta := is.images[id]
	if imageMeta == nil {
		return nil, fmt.Errorf("unrecognized image ID %s", id.String())
	}
	delete(is.images, id)
	is.fs.Delete(digest.Digest(id))

	if imageMeta.layer != nil {
		return is.ls.Release(imageMeta.layer)
	}
	return nil, nil
}

func (is *store) SetParent(id, parent ID) error {
	is.Lock()
	parentMeta := is.images[parent]
	if parentMeta == nil {
		return fmt.Errorf("unknown parent image ID %s", parent.String())
	}
	parentMeta.children[id] = struct{}{}
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

func (is *store) Children(id ID) []ID {
	is.Lock()
	defer is.Unlock()

	return is.children(id)
}

func (is *store) children(id ID) []ID {
	var ids []ID
	if is.images[id] != nil {
		for id := range is.images[id].children {
			ids = append(ids, id)
		}
	}
	return ids
}

func (is *store) Heads() map[ID]*Image {
	return is.imagesMap(false)
}

func (is *store) Map() map[ID]*Image {
	return is.imagesMap(true)
}

func (is *store) imagesMap(all bool) map[ID]*Image {
	is.Lock()
	defer is.Unlock()

	images := make(map[ID]*Image)

	for id := range is.images {
		if !all && len(is.children(id)) > 0 {
			continue
		}
		img, err := is.Get(id)
		if err != nil {
			logrus.Errorf("invalid image access: %q, error: %q", id, err)
			continue
		}
		images[id] = img
	}
	return images
}
