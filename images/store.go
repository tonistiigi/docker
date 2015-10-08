package images

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
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
	ids  map[ID]struct{}
	fs   *fs
}

type migratoryLayerStore interface {
	RegisterOnDisk(string, layers.ID, string) (layers.Layer, layers.DiffID, error)
}

const (
	graphDirName      = "graph"
	imagesDirName     = "images"
	tarDataFileName   = "tar-data.json.gz"
	migrationFileName = "graph-to-images-migration.json"
)

// NewImageStore returns new store object for given LayerStore
func NewImageStore(root string, ls layers.LayerStore) (Store, error) {
	fs, err := newFSStore(filepath.Join(root, imagesDirName))
	if err != nil {
		return nil, err
	}

	is := &store{
		root: root,
		ls:   ls,
		ids:  make(map[ID]struct{}),
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
		is.retainLayers(img.DiffIDs, id.String())
		is.ids[ID(id)] = struct{}{}
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

	dgst, err := digest.FromBytes(config)
	if err != nil {
		return "", err
	}
	imageID := ID(dgst)

	is.Lock()
	defer is.Unlock()

	if _, exists := is.ids[imageID]; exists {
		return imageID, nil
	}

	if err := is.retainLayers(img.DiffIDs, string(imageID)); err != nil {
		return "", err
	}

	_, err = is.fs.Set(config) // todo: validate or reorganize
	if err != nil {
		return "", err
	}

	is.ids[imageID] = struct{}{}

	return imageID, nil
}

func (is *store) retainLayers(dgsts []layers.DiffID, key string) error {
	if len(dgsts) == 0 {
		return errors.New("Invalid image config. No layer digests.")
	}

	for i := range dgsts {
		layerID, err := layers.LayerID("", dgsts[:i+1]...) // todo: this can be optimized
		if err != nil {
			return err
		}
		if _, err := is.ls.Get(layerID); err != nil {
			return err
		}
	}
	return nil
}

func (is *store) Get(id ID) (*Image, error) {
	// todo: validate digest(maybe imageID)

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

func (is *store) migrateV1Images() error {
	if _, ok := is.ls.(migratoryLayerStore); !ok {
		return nil
	}

	mfile := filepath.Join(is.root, migrationFileName)
	graphDir := filepath.Join(is.root, graphDirName)

	mappings := make(map[string]ID)

	f, err := os.Open(mfile)
	if err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		err := json.NewDecoder(f).Decode(&mappings)
		if err != nil {
			f.Close()
			return err
		}
		f.Close()
	}

	dir, err := ioutil.ReadDir(graphDir)
	if err != nil {
		return err
	}
	// var ids = []string{}
	for _, v := range dir {
		v1ID := v.Name()
		if err := ValidateID(v1ID); err != nil {
			continue
		}
		if _, exists := mappings[v1ID]; exists {
			continue
		} else {
			if err := is.migrateV1Image(v1ID, mappings); err != nil {
				// todo: fail or allow broken chains?b
				continue
			}
		}
	}

	f, err = os.OpenFile(mfile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(mappings); err != nil {
		return err
	}

	return nil
}

func (is *store) migrateV1Image(id string, mappings map[string]ID) (err error) {
	defer func() {
		if err != nil {
			logrus.Errorf("migration failed for %v, err: %v", id, err)
		}
	}()

	jsonFile := filepath.Join(is.root, graphDirName, id, "json")
	imageJSON, err := ioutil.ReadFile(jsonFile)
	if err != nil {
		return err
	}
	var parent struct{ Parent string }
	if err := json.Unmarshal(imageJSON, &parent); err != nil {
		return err
	}

	var parentID ID
	if parent.Parent != "" {
		var exists bool
		if parentID, exists = mappings[parent.Parent]; !exists {
			if err := is.migrateV1Image(parent.Parent, mappings); err != nil {
				// todo: fail or allow broken chains?
				return err
			}
			parentID = mappings[id]
		}
	}

	migratoryLayerStore, exists := is.ls.(migratoryLayerStore)
	if !exists {
		return errors.New("migration not supported")
	}

	var layerDigests []layers.DiffID

	if parentID != "" {
		parentImg, err := is.Get(parentID)
		if err != nil {
			return err
		}

		layerDigests = parentImg.DiffIDs

	}

	parentLayer, err := layers.LayerID("", layerDigests...)
	if err != nil {
		return err
	}

	_, diffID, err := migratoryLayerStore.RegisterOnDisk(id, parentLayer, filepath.Join(filepath.Join(is.root, graphDirName, id, tarDataFileName)))
	if err != nil {
		return err
	}

	// todo: handle empty layers

	// todo: blobsumStore.add(checksum, diffid)

	// todo: new makeConfig function

	// todo: fallback default fields removal for old clients

	layerDigests = append(layerDigests, diffID)
	config, err := ConfigFromV1Config(imageJSON, layerDigests)
	if err != nil {
		return err
	}
	strongID, err := is.Create(config)
	if err != nil {
		return err
	}

	mappings[id] = strongID
	return
}

// ConfigFromV1Config creates an image config from the legacy V1 config format.
func ConfigFromV1Config(imageJSON []byte, layerDigests []layers.DiffID) ([]byte, error) {
	var c map[string]*json.RawMessage
	if err := json.Unmarshal(imageJSON, &c); err != nil {
		return nil, err
	}

	delete(c, "id")
	delete(c, "parent")
	delete(c, "Size") // Size is calculated from data on disk and is inconsitent
	delete(c, "parent_id")
	delete(c, "layer_id")

	c["layer_digests"] = rawJSON(layerDigests)

	return json.Marshal(c)
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}
