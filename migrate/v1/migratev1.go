package v1

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/tag"
	"github.com/jfrazelle/go/canonical/json"
)

var validHex = regexp.MustCompile(`^([a-f0-9]{64})$`)

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

func Migrate(root, driverName string, ls layer.Store, is images.Store, ts tag.Store) error {
	mappings := make(map[string]images.ID)

	if err := migrateImages(root, ls, is, mappings); err != nil {
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

func migrateImages(root string, ls layer.Store, is images.Store, mappings map[string]images.ID) error {
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
		if err := ValidateV1ID(v1ID); err != nil {
			continue
		}
		if _, exists := mappings[v1ID]; exists {
			continue
		} else {
			if err := migrateImage(v1ID, root, ls, is, mappings); err != nil {
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

func migrateContainers(root string, ls layer.Store, is images.Store, imageMappings map[string]images.ID) error {
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

func migrateTags(root, driverName string, ts tag.Store, mappings map[string]images.ID) error {
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

func migrateImage(id, root string, ls layer.Store, is images.Store, mappings map[string]images.ID) (err error) {
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

	var parentID images.ID
	if parent.Parent != "" {
		var exists bool
		if parentID, exists = mappings[parent.Parent]; !exists {
			if err := migrateImage(parent.Parent, root, ls, is, mappings); err != nil {
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

	parentLayer := layer.CreateID(layerDigests)

	layer, err := migratoryLayerStore.RegisterByGraphID(id, parentLayer, filepath.Join(filepath.Join(root, graphDirName, id, tarDataFileName)))
	if err != nil {
		return err
	}

	h, err := HistoryFromV1Config(imageJSON)
	if err != nil {
		return err
	}
	h.Size, err = layer.DiffSize()
	if err != nil {
		return err
	}
	history = append(history, h)

	config, err := ConfigFromV1Config(imageJSON, layer, history)
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
	h.CreatedBy = strings.Join(v1Image.ContainerConfig.Cmd.Slice(), " ")
	h.Comment = v1Image.Comment

	return h, nil
}

func CreateV1ID(v1Image images.ImageV1, layerID layer.ID, parent digest.Digest) (digest.Digest, error) {
	v1Image.ID = ""
	v1JSON, err := json.Marshal(v1Image)
	if err != nil {
		return "", err
	}

	var config map[string]*json.RawMessage
	if err := json.Unmarshal(v1JSON, &config); err != nil {
		return "", err
	}

	// FIXME: note that this is slightly incompatible with RootFS logic
	config["layer_id"] = rawJSON(layerID)
	if parent != "" {
		config["parent"] = rawJSON(parent)
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	logrus.Debugf("CreateV1ID %s", configJSON)

	return digest.FromBytes(configJSON)
}

// addLayerDiffIDs creates a slice containing all layer diff IDs for the given
// layer, ordered from base to top-most.
func addLayerDiffIDs(l layer.Layer) []layer.DiffID {
	parent := l.Parent()
	if parent == nil {
		return []layer.DiffID{l.DiffID()}
	}
	return append(addLayerDiffIDs(parent), l.DiffID())
}

// ConfigFromV1Config creates an image config from the legacy V1 config format.
func ConfigFromV1Config(imageJSON []byte, l layer.Layer, history []images.History) ([]byte, error) {
	layerDigests := addLayerDiffIDs(l)

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

// V1ConfigFromConfig creates an legacy V1 image config from an Image struct
func V1ConfigFromConfig(img *images.Image, v1ID, parentV1ID string) ([]byte, error) {
	// Top-level v1compatibility string should be a modified version of the
	// image config.
	var configAsMap map[string]*json.RawMessage
	if err := json.Unmarshal(img.RawJSON(), &configAsMap); err != nil {
		return nil, err
	}

	// Delete fields that didn't exist in old manifest
	delete(configAsMap, "rootfs")
	delete(configAsMap, "history")
	configAsMap["id"] = rawJSON(v1ID)
	if parentV1ID != "" {
		configAsMap["parent"] = rawJSON(parentV1ID)
	}

	return json.Marshal(configAsMap)
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}

// ValidateID checks whether an ID string is a valid image ID.
func ValidateV1ID(id string) error {
	if ok := validHex.MatchString(id); !ok {
		return fmt.Errorf("image ID '%s' is invalid ", id)
	}
	return nil
}
