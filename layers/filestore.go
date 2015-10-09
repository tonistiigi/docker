package layers

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
)

var stringIDRegexp = regexp.MustCompile(`^([a-f0-9]{64})$`)

type fileMetadataStore struct {
	root string
}

func NewFileMetadataStore(root string) MetadataStore {
	return &fileMetadataStore{
		root: root,
	}
}

func (fms *fileMetadataStore) getLayerDirectory(layer ID) string {
	return filepath.Join(fms.root, digest.Digest(layer).String())
}

func (fms *fileMetadataStore) getLayerFilename(layer ID, filename string) string {
	return filepath.Join(fms.getLayerDirectory(layer), filename)
}

func (fms *fileMetadataStore) SetSize(layer ID, size int64) error {
	if err := os.MkdirAll(fms.getLayerDirectory(layer), 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("%d", size)
	return ioutil.WriteFile(fms.getLayerFilename(layer, "size"), []byte(content), 0644)
}

func (fms *fileMetadataStore) SetParent(layer, parent ID) error {
	if err := os.MkdirAll(fms.getLayerDirectory(layer), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(fms.getLayerFilename(layer, "parent"), []byte(digest.Digest(parent).String()), 0644)
}

func (fms *fileMetadataStore) SetDiffID(layer ID, diff DiffID) error {
	if err := os.MkdirAll(fms.getLayerDirectory(layer), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(fms.getLayerFilename(layer, "diff"), []byte(digest.Digest(diff).String()), 0644)
}

func (fms *fileMetadataStore) SetCacheID(layer ID, cacheID string) error {
	if err := os.MkdirAll(fms.getLayerDirectory(layer), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(fms.getLayerFilename(layer, "cache-id"), []byte(cacheID), 0644)
}

func (fms *fileMetadataStore) SetTarSplit(layer ID, content io.Reader) error {
	if err := os.MkdirAll(fms.getLayerDirectory(layer), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(fms.getLayerFilename(layer, "tar-split.json.gz"), os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	_, err = io.Copy(f, content)

	return err
}

func (fms *fileMetadataStore) GetSize(layer ID) (int64, error) {
	content, err := ioutil.ReadFile(fms.getLayerFilename(layer, "size"))
	if err != nil {
		return 0, err
	}

	size, err := strconv.ParseInt(string(content), 10, 64)
	if err != nil {
		return 0, err
	}

	return size, nil
}

func (fms *fileMetadataStore) GetParent(layer ID) (ID, error) {
	content, err := ioutil.ReadFile(fms.getLayerFilename(layer, "parent"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	dgst, err := digest.ParseDigest(string(content))
	if err != nil {
		return "", err
	}

	return ID(dgst), nil
}

func (fms *fileMetadataStore) GetDiffID(layer ID) (DiffID, error) {
	content, err := ioutil.ReadFile(fms.getLayerFilename(layer, "diff"))
	if err != nil {
		return "", err
	}

	dgst, err := digest.ParseDigest(string(content))
	if err != nil {
		return "", err
	}

	return DiffID(dgst), nil
}

func (fms *fileMetadataStore) GetCacheID(layer ID) (string, error) {
	content, err := ioutil.ReadFile(fms.getLayerFilename(layer, "cache-id"))
	if err != nil {
		return "", err
	}

	if !stringIDRegexp.MatchString(string(content)) {
		return "", errors.New("invalid cache id value")
	}

	return string(content), nil
}

func (fms *fileMetadataStore) GetTarSplit(layer ID) (io.ReadCloser, error) {
	return os.Open(fms.getLayerFilename(layer, "tar-split.json.gz"))
}

func (fms *fileMetadataStore) List() ([]ID, []string, error) {
	fileInfos, err := ioutil.ReadDir(fms.root)
	if err != nil {
		return nil, nil, err
	}

	var ids []ID
	var mounts []string
	for _, fi := range fileInfos {
		if fi.IsDir() {
			dgst, err := digest.ParseDigest(fi.Name())
			if err == nil {
				ids = append(ids, ID(dgst))
			} else if stringIDRegexp.MatchString(fi.Name()) {
				mounts = append(mounts, fi.Name())
			} else {
				logrus.Debugf("Ignoring invalid directory %s", fi.Name())
			}
		}
	}

	return ids, mounts, nil
}
