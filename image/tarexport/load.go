package tarexport

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/migrate/v1"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/symlink"
)

func (l *tarexporter) Load(inTar io.ReadCloser, outStream io.Writer) error {
	tmpDir, err := ioutil.TempDir("", "docker-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := chrootarchive.Untar(inTar, tmpDir, nil); err != nil {
		return err
	}
	// read manifest, if no file then load in legacy mode
	manifestPath, err := safePath(tmpDir, manifestFileName)
	if err != nil {
		return err
	}
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return l.legacyLoad(tmpDir, outStream)
		}
		return manifestFile.Close()
	}
	defer manifestFile.Close()

	var manifest []manifestItem
	if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		return err
	}

	var layers []layer.DiffID
	for _, m := range manifest {
		for _, layerFile := range m.Layers {
			layerPath, err := safePath(tmpDir, layerFile)
			if err != nil {
				return err
			}
			newLayer, err := l.loadLayer(layerPath, layers)
			if err != nil {
				return err
			}
			layers = append(layers, newLayer.DiffID())
			defer l.ls.Release(newLayer)
		}

		configPath, err := safePath(tmpDir, m.Config)
		if err != nil {
			return err
		}
		config, err := ioutil.ReadFile(configPath)
		if err != nil {
			return err
		}
		imgID, err := l.is.Create(config)
		if err != nil {
			return err
		}

		for _, repoTag := range m.RepoTags {
			named, err := reference.ParseNamed(repoTag)
			if err != nil {
				return err
			}
			if ref, ok := named.(reference.NamedTagged); !ok {
				return fmt.Errorf("invalid tag %q", repoTag)
			} else {
				l.setLoadedTag(ref, imgID, outStream)
			}
		}

	}

	return nil
}

func (l *tarexporter) loadLayer(filename string, parentDiffs []layer.DiffID) (layer.Layer, error) {
	rawTar, err := os.Open(filename)
	if err != nil {
		logrus.Debugf("Error reading embedded tar: %v", err)
		return nil, err
	}
	inflatedLayerData, err := archive.DecompressStream(rawTar)
	if err != nil {
		return nil, err
	}

	defer rawTar.Close()
	defer inflatedLayerData.Close()

	parentLayerID := layer.CreateID(parentDiffs)

	return l.ls.Register(inflatedLayerData, parentLayerID)
}

func (l *tarexporter) setLoadedTag(ref reference.NamedTagged, imgID image.ID, outStream io.Writer) error {
	if prevID, err := l.ts.Get(ref); err == nil && prevID != imgID {
		fmt.Fprintf(outStream, "The image %s:%s already exists, renaming the old one with ID %s to empty string\n", ref.String(), string(prevID)) // todo: this message is wrong in case of multiple tags
	}

	if err := l.ts.Add(ref, imgID, true); err != nil {
		return err
	}
	return nil
}

func (l *tarexporter) legacyLoad(tmpDir string, outStream io.Writer) error {
	legacyLoadedMap := make(map[string]image.ID)

	dirs, err := ioutil.ReadDir(tmpDir)
	if err != nil {
		return err
	}

	// every dir represents an image
	for _, d := range dirs {
		if d.IsDir() {
			if err := l.legacyLoadImage(d.Name(), tmpDir, legacyLoadedMap); err != nil {
				return err
			}
		}
	}

	// load tags from repositories file
	repositoriesPath, err := safePath(tmpDir, legacyRepositoriesFileName)
	if err != nil {
		return err
	}
	repositoriesFile, err := os.Open(repositoriesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return repositoriesFile.Close()
	}
	defer repositoriesFile.Close()

	repositories := make(map[string]map[string]string)
	if err := json.NewDecoder(repositoriesFile).Decode(&repositories); err != nil {
		return err
	}

	for name, tagMap := range repositories {
		for tag, oldID := range tagMap {
			if imgID, ok := legacyLoadedMap[oldID]; !ok {
				return fmt.Errorf("invalid target ID: %v", oldID)
			} else {

				named, err := reference.WithName(name)
				if err != nil {
					return err
				}
				ref, err := reference.WithTag(named, tag)
				if err != nil {
					return err
				}

				l.setLoadedTag(ref, imgID, outStream)

			}
		}
	}

	return nil
}

func (l *tarexporter) legacyLoadImage(oldID, sourceDir string, loadedMap map[string]image.ID) error {
	if _, loaded := loadedMap[oldID]; loaded {
		return nil
	}
	configPath, err := safePath(sourceDir, filepath.Join(oldID, legacyConfigFileName))
	if err != nil {
		return err
	}
	imageJSON, err := ioutil.ReadFile(configPath)
	if err != nil {
		logrus.Debugf("Error reading json: %v", err)
		return err
	}

	var img struct{ Parent string }
	if err := json.Unmarshal(imageJSON, &img); err != nil {
		return err
	}

	var parentID image.ID
	if img.Parent != "" {
		for {
			var loaded bool
			if parentID, loaded = loadedMap[img.Parent]; !loaded {
				if err := l.legacyLoadImage(img.Parent, sourceDir, loadedMap); err != nil {
					return err
				}
			} else {
				break
			}
		}
	}

	// todo: try to connect with migrate code
	var layerDigests []layer.DiffID
	var history []image.History

	if parentID != "" {
		parentImg, err := l.is.Get(parentID)
		if err != nil {
			return err
		}

		layerDigests = parentImg.RootFS.DiffIDs
		history = parentImg.History
	}

	layerPath, err := safePath(sourceDir, filepath.Join(oldID, legacyLayerFileName))
	if err != nil {
		return err
	}
	newLayer, err := l.loadLayer(layerPath, layerDigests)
	if err != nil {
		return err
	}

	h, err := v1.HistoryFromV1Config(imageJSON)
	if err != nil {
		return err
	}
	h.Size, err = newLayer.DiffSize()
	if err != nil {
		return err
	}
	history = append(history, h)

	config, err := v1.ConfigFromV1Config(imageJSON, newLayer, history)
	if err != nil {
		return err
	}
	imgID, err := l.is.Create(config)
	if err != nil {
		return err
	}

	_, err = l.ls.Release(newLayer)
	if err != nil {
		return err
	}

	if parentID != "" {
		if err := l.is.SetParent(imgID, parentID); err != nil {
			return err
		}
	}

	loadedMap[oldID] = imgID
	return nil
}

func safePath(base, path string) (string, error) {
	return symlink.FollowSymlinkInScope(filepath.Join(base, path), base)
}
