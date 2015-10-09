package images

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/layers"
)

type migratoryLayerStore interface {
	RegisterByGraphID(string, layers.ID, string) (layers.Layer, error)
	MountByGraphID(string, string, layers.ID) (layers.RWLayer, error)
}

const (
	graphDirName         = "graph"
	tarDataFileName      = "tar-data.json.gz"
	migrationFileName    = "graph-to-images-migration.json"
	containersDirName    = "containers"
	configFileNameLegacy = "config.json"
	configFileName       = "config.v2.json"
)

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

	return is.migrateV1Containers(mappings)
}

func (is *store) migrateV1Containers(imageMappings map[string]ID) error {
	containersDir := path.Join(is.root, containersDirName)
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

		migratoryLayerStore, exists := is.ls.(migratoryLayerStore)
		if !exists {
			return errors.New("migration not supported")
		}

		img, err := is.Get(imageID)
		if err != nil {
			return err
		}

		layer, err := img.GetTopLayer()
		if err != nil {
			return err
		}

		_, err = migratoryLayerStore.MountByGraphID(id, id, layer.ID())
		if err != nil {
			return err
		}

		err = is.ls.Unmount(id)
		if err != nil {
			return err
		}

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
			parentID = mappings[parent.Parent]
		}
	}

	migratoryLayerStore, exists := is.ls.(migratoryLayerStore)
	if !exists {
		return errors.New("migration not supported")
	}

	var layerDigests []layers.DiffID
	var history []History

	if parentID != "" {
		parentImg, err := is.Get(parentID)
		if err != nil {
			return err
		}

		layerDigests = parentImg.DiffIDs
		history = parentImg.History
	}

	parentLayer, err := layers.LayerID("", layerDigests...)
	if err != nil {
		return err
	}

	layer, err := migratoryLayerStore.RegisterByGraphID(id, parentLayer, filepath.Join(filepath.Join(is.root, graphDirName, id, tarDataFileName)))
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

	_, err = is.ls.Release(layer)
	if err != nil {
		return err
	}

	if parentID != "" {
		if err := is.fs.SetMetadata(digest.Digest(strongID), "parent", []byte(parentID)); err != nil {
			return err
		}
	}

	mappings[id] = strongID
	return
}

// HistoryFromV1Config creates a History struct from v1 configuration JSON
func HistoryFromV1Config(imageJSON []byte) (History, error) {
	h := History{}
	var v1Image ImageV1
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
func ConfigFromV1Config(imageJSON []byte, layerDigests []layers.DiffID, history []History) ([]byte, error) {
	var c map[string]*json.RawMessage
	if err := json.Unmarshal(imageJSON, &c); err != nil {
		return nil, err
	}

	delete(c, "id")
	delete(c, "parent")
	delete(c, "Size") // Size is calculated from data on disk and is inconsitent
	delete(c, "parent_id")
	delete(c, "layer_id")

	c["diff_ids"] = rawJSON(layerDigests)
	c["history"] = rawJSON(history)

	return json.Marshal(c)
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}
