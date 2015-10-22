package tarexport

import (
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/migrate/v1"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/tag"
)

type Loader interface {
	Load(io.ReadCloser, io.Writer) error
	// Load(net.Context, io.ReadCloser, <- chan StatusMessage) error
}

type loader struct {
	is images.Store
	ls layer.Store
	ts tag.Store
}

func NewLoader(is images.Store, ls layer.Store, ts tag.Store) (Loader, error) {
	if is == nil || ls == nil || ts == nil {
		return nil, errors.New("Invalid loader configuration")
	}
	return &loader{
		is: is,
		ls: ls,
		ts: ts,
	}, nil
}

func (l *loader) Load(inTar io.ReadCloser, outStream io.Writer) error {
	tmpImageDir, err := ioutil.TempDir("", "docker-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpImageDir)

	repoDir := filepath.Join(tmpImageDir, "repo")

	if err := chrootarchive.Untar(inTar, repoDir, nil); err != nil {
		return err
	}

	// todo: detect legacy load

	legacyLoadedMap := make(map[string]images.ID)

	dirs, err := ioutil.ReadDir(repoDir)
	if err != nil {
		return err
	}

	for _, d := range dirs {
		if d.IsDir() {
			if err := l.legacyRecursiveLoad(d.Name(), tmpImageDir, legacyLoadedMap); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *loader) legacyRecursiveLoad(oldID, sourceDir string, loadedMap map[string]images.ID) error {
	if _, loaded := loadedMap[oldID]; loaded {
		return nil
	}

	imageJSON, err := ioutil.ReadFile(filepath.Join(sourceDir, "repo", oldID, "json"))
	if err != nil {
		logrus.Debugf("Error reading json: %v", err)
		return err
	}

	var img struct{ Parent string }
	if err := json.Unmarshal(imageJSON, &img); err != nil {
		return err
	}

	var parentID images.ID
	if img.Parent != "" {
		for {
			var loaded bool
			if parentID, loaded = loadedMap[img.Parent]; !loaded {
				if err := l.legacyRecursiveLoad(img.Parent, sourceDir, loadedMap); err != nil {
					return err
				}
			} else {
				break
			}

		}
	}

	tar, err := os.Open(filepath.Join(sourceDir, "repo", oldID, "layer.tar"))
	if err != nil {
		logrus.Debugf("Error reading embedded tar: %v", err)
		return err
	}
	defer tar.Close()

	// todo: try to connect with migrate code
	var layerDigests []layer.DiffID
	var history []images.History

	if parentID != "" {
		parentImg, err := l.is.Get(parentID)
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

	newLayer, err := l.ls.Register(tar, parentLayer)
	if err != nil {
		return err
	}

	layerDigests = append(layerDigests, newLayer.DiffID())

	h, err := v1.HistoryFromV1Config(imageJSON)
	if err != nil {
		return err
	}
	history = append(history, h)

	config, err := v1.ConfigFromV1Config(imageJSON, layerDigests, history)
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
