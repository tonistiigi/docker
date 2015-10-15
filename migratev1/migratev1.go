package migratev1

// FIXME: try moving this out of image package

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/tag"
	"github.com/jfrazelle/go/canonical/json"
)

type migratoryLayerStore interface {
	RegisterByGraphID(string, layer.ID, string) (layer.Layer, error)
	MountByGraphID(string, string, layer.ID) (layer.RWLayer, error)
}

const (
	graphDirName         = "graph"
	tarDataFileName      = "tar-data.json.gz"
	migrationFileName    = "graph-to-images-migration.json"
	containersDirName    = "containers"
	configFileNameLegacy = "config.json"
	configFileName       = "config.v2.json"
)

var (
	errUnsupported = errors.New("migration is not supported")
)

func MigrateV1(root string, ls layer.Store, is images.Store, ts tag.Store) error {
	mappings := make(map[string]images.ID)

	if err := migrateV1Images(root, ls, is, mappings); err != nil {
		return err
	}

	if err := migrateV1Containers(root, ls, is, mappings); err != nil {
		return err
	}

	return nil
}

func migrateV1Images(root string, ls layer.Store, is images.Store, mappings map[string]images.ID) error {
	if _, ok := ls.(migratoryLayerStore); !ok {
		return errUnsupported
	}

	mfile := filepath.Join(root, migrationFileName)
	graphDir := filepath.Join(root, graphDirName)

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
		if err := images.ValidateID(v1ID); err != nil {
			continue
		}
		if _, exists := mappings[v1ID]; exists {
			continue
		} else {
			if err := migrateV1Image(v1ID, root, ls, is, mappings); err != nil {
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

func migrateV1Containers(root string, ls layer.Store, is images.Store, imageMappings map[string]images.ID) error {
	containersDir := path.Join(root, containersDirName)
	dir, err := ioutil.ReadDir(containersDir)
	if err != nil {
		return err
	}
	// var ids = []string{}
	for _, v := range dir {
		id := v.Name()

		if _, err := os.Stat(path.Join(containersDir, id, configFileName)); err == nil {
			continue
		}

		containerJSON, err := ioutil.ReadFile(path.Join(containersDir, id, configFileNameLegacy))
		if err != nil {
			return err
		}

		var c map[string]*json.RawMessage
		if err := json.Unmarshal(containerJSON, &c); err != nil {
			return err
		}

		imageStrJSON, ok := c["Image"]
		if !ok {
			return fmt.Errorf("invalid container configuration for %v", id)
		}

		var image string
		if err := json.Unmarshal([]byte(*imageStrJSON), &image); err != nil {
			return err
		}

		imageID, ok := imageMappings[image]
		if !ok {
			return fmt.Errorf("image not migrated %v", imageID)
		}

		c["Image"] = rawJSON(imageID)

		containerJSON, err = json.Marshal(c)
		if err != nil {
			return err
		}

		if err := ioutil.WriteFile(path.Join(containersDir, id, configFileName), containerJSON, 0600); err != nil {
			return err
		}

		migratoryLayerStore, exists := ls.(migratoryLayerStore)
		if !exists {
			return errors.New("migration not supported")
		}

		img, err := is.Get(imageID)
		if err != nil {
			return err
		}

		layerID, err := img.GetTopLayerID()
		if err != nil {
			return err
		}

		_, err = migratoryLayerStore.MountByGraphID(id, id, layerID)
		if err != nil {
			return err
		}

		err = ls.Unmount(id)
		if err != nil {
			return err
		}

	}
	return nil
}

// func migrateV1Tags(mappings map[string]images.ID) error {
// 	return nil
// }

func migrateV1Image(id, root string, ls layer.Store, is images.Store, mappings map[string]images.ID) (err error) {
	defer func() {
		if err != nil {
			logrus.Errorf("migration failed for %v, err: %v", id, err)
		}
	}()

	jsonFile := filepath.Join(root, graphDirName, id, "json")
	imageJSON, err := ioutil.ReadFile(jsonFile)
	if err != nil {
		return err
	}
	var parent struct{ Parent string }
	if err := json.Unmarshal(imageJSON, &parent); err != nil {
		return err
	}

	var parentID images.ID
	if parent.Parent != "" {
		var exists bool
		if parentID, exists = mappings[parent.Parent]; !exists {
			if err := migrateV1Image(parent.Parent, root, ls, is, mappings); err != nil {
				// todo: fail or allow broken chains?
				return err
			}
			parentID = mappings[parent.Parent]
		}
	}

	migratoryLayerStore, exists := ls.(migratoryLayerStore)
	if !exists {
		return errUnsupported
	}

	var layerDigests []layer.DiffID
	var history []images.History

	if parentID != "" {
		parentImg, err := is.Get(parentID)
		if err != nil {
			return err
		}

		layerDigests = parentImg.RootFS.DiffIDs
		history = parentImg.History
	}

	parentLayer, err := layer.CreateID("", layerDigests...)
	if err != nil {
		return err
	}

	layer, err := migratoryLayerStore.RegisterByGraphID(id, parentLayer, filepath.Join(filepath.Join(root, graphDirName, id, tarDataFileName)))
	if err != nil {
		return err
	}

	// todo: handle empty layers

	// todo: blobsumStore.add(checksum, diffid)

	// todo: new makeConfig function

	// todo: fallback default fields removal for old clients

	layerDigests = append(layerDigests, layer.DiffID())

	h, err := HistoryFromV1Config(imageJSON)
	if err != nil {
		return err
	}
	history = append(history, h)

	config, err := ConfigFromV1Config(imageJSON, layerDigests, history)
	if err != nil {
		return err
	}
	strongID, err := is.Create(config)
	if err != nil {
		return err
	}

	_, err = ls.Release(layer)
	if err != nil {
		return err
	}

	if parentID != "" {
		if err := is.SetParent(strongID, parentID); err != nil {
			return err
		}
	}

	mappings[id] = strongID
	return
}

// HistoryFromV1Config creates a History struct from v1 configuration JSON
func HistoryFromV1Config(imageJSON []byte) (images.History, error) {
	h := images.History{}
	var v1Image images.ImageV1
	if err := json.Unmarshal(imageJSON, &v1Image); err != nil {
		return h, err
	}

	h.Author = v1Image.Author
	h.Created = v1Image.Created
	h.Description = strings.Join(v1Image.ContainerConfig.Cmd.Slice(), " ")

	if len(v1Image.Comment) > 0 {
		h.Description += "(" + v1Image.Comment + ")"
	}

	return h, nil
}

// ConfigFromV1Config creates an image config from the legacy V1 config format.
func ConfigFromV1Config(imageJSON []byte, layerDigests []layer.DiffID, history []images.History) ([]byte, error) {
	var c map[string]*json.RawMessage
	if err := json.Unmarshal(imageJSON, &c); err != nil {
		return nil, err
	}

	delete(c, "id")
	delete(c, "parent")
	delete(c, "Size") // Size is calculated from data on disk and is inconsitent
	delete(c, "parent_id")
	delete(c, "layer_id")

	c["rootfs"] = rawJSON(&images.RootFS{Type: "layers", DiffIDs: layerDigests})
	c["history"] = rawJSON(history)

	return json.MarshalCanonical(c)
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}
