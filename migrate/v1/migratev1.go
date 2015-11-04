package v1

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	"encoding/json"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/image"
	imagev1 "github.com/docker/docker/image/v1"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/tag"
)

type migratoryLayerStore interface {
	RegisterByGraphID(string, layer.ID, string) (layer.Layer, error)
	MountByGraphID(string, string, layer.ID) (layer.RWLayer, error)
}

const (
	graphDirName                 = "graph"
	tarDataFileName              = "tar-data.json.gz"
	migrationFileName            = ".migration-v1-images.json"
	migrationTagsFileName        = ".migration-v1-tags"
	containersDirName            = "containers"
	configFileNameLegacy         = "config.json"
	configFileName               = "config.v2.json"
	repositoriesFilePrefixLegacy = "repositories-"
	repositoriesFilePrefix       = "repositories.v2-"
)

var (
	errUnsupported = errors.New("migration is not supported")
)

func Migrate(root, driverName string, ls layer.Store, is image.Store, ts tag.Store, ms metadata.Store) error {
	mappings := make(map[string]image.ID)

	if err := migrateImages(root, ls, is, ms, mappings); err != nil {
		return err
	}

	if err := migrateContainers(root, ls, is, mappings); err != nil {
		return err
	}

	if err := migrateTags(root, driverName, ts, mappings); err != nil {
		return err
	}

	return nil
}

func migrateImages(root string, ls layer.Store, is image.Store, ms metadata.Store, mappings map[string]image.ID) error {
	if _, ok := ls.(migratoryLayerStore); !ok {
		return errUnsupported
	}

	graphDir := filepath.Join(root, graphDirName)
	if _, err := os.Lstat(graphDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	mfile := filepath.Join(root, migrationFileName)
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
		if err := imagev1.ValidateID(v1ID); err != nil {
			continue
		}
		if _, exists := mappings[v1ID]; exists {
			continue
		} else {
			if err := migrateImage(v1ID, root, ls, is, ms, mappings); err != nil {
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

func migrateContainers(root string, ls layer.Store, is image.Store, imageMappings map[string]image.ID) error {
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

		_, err = migratoryLayerStore.MountByGraphID(id, id, img.GetTopLayerID())
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

func migrateTags(root, driverName string, ts tag.Store, mappings map[string]image.ID) error {
	migrationFile := filepath.Join(root, migrationTagsFileName)
	if _, err := os.Lstat(migrationFile); !os.IsNotExist(err) {
		return err
	}

	type repositories struct {
		Repositories map[string]map[string]string
	}

	var repos repositories

	f, err := os.Open(filepath.Join(root, repositoriesFilePrefixLegacy+driverName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&repos); err != nil {
		return err
	}

	for name, repo := range repos.Repositories {
		for tag, id := range repo {
			if strongID, exists := mappings[id]; exists {
				ref, err := reference.WithName(name)
				if err != nil {
					return err
				}
				if dgst, err := digest.ParseDigest(tag); err == nil {
					ref, err = reference.WithDigest(ref, dgst)
					if err != nil {
						return err
					}
				} else {
					ref, err = reference.WithTag(ref, tag)
					if err != nil {
						return err
					}
				}
				if err := ts.Add(ref, strongID, false); err != nil {
					logrus.Errorf("can't migrate tag %q for %q, err: %q", ref.String(), strongID, err)
				}
			}
		}
	}

	mf, err := os.Create(migrationFile)
	if err != nil {
		return err
	}
	mf.Close()

	return nil
}

func migrateImage(id, root string, ls layer.Store, is image.Store, ms metadata.Store, mappings map[string]image.ID) (err error) {
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
	var parent struct {
		Parent   string
		ParentID digest.Digest `json:"parent_id"`
	}
	if err := json.Unmarshal(imageJSON, &parent); err != nil {
		return err
	}
	if parent.Parent == "" && parent.ParentID != "" { // v1.9
		parent.Parent = parent.ParentID.Hex()
	}

	var parentID image.ID
	if parent.Parent != "" {
		var exists bool
		if parentID, exists = mappings[parent.Parent]; !exists {
			if err := migrateImage(parent.Parent, root, ls, is, ms, mappings); err != nil {
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
	var history []image.History

	if parentID != "" {
		parentImg, err := is.Get(parentID)
		if err != nil {
			return err
		}

		layerDigests = parentImg.RootFS.DiffIDs
		history = parentImg.History
	}

	parentLayer := layer.CreateID(layerDigests)

	layer, err := migratoryLayerStore.RegisterByGraphID(id, parentLayer, filepath.Join(filepath.Join(root, graphDirName, id, tarDataFileName)))
	if err != nil {
		return err
	}

	h, err := imagev1.HistoryFromConfig(imageJSON, false)
	if err != nil {
		return err
	}
	history = append(history, h)

	config, err := imagev1.ConfigFromV1Config(imageJSON, layer, history)
	if err != nil {
		return err
	}
	strongID, err := is.Create(config)
	if err != nil {
		return err
	}

	if parentID != "" {
		if err := is.SetParent(strongID, parentID); err != nil {
			return err
		}
	}

	checksum, err := ioutil.ReadFile(filepath.Join(root, graphDirName, id, "checksum"))
	if err == nil { // best effort
		dgst, err := digest.ParseDigest(string(checksum))
		if err == nil {
			blobSumService := metadata.NewBlobSumService(ms)
			blobSumService.Add(layer.DiffID(), dgst)
		}
	}
	_, err = ls.Release(layer)
	if err != nil {
		return err
	}

	mappings[id] = strongID
	return
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}
