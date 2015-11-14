package image

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
)

// IDWalkFunc is function called by StoreBackend.Walk
type IDWalkFunc func(id ID) error

// StoreBackend provides interface for image.Store persistence
type StoreBackend interface {
	Walk(f IDWalkFunc) error
	Get(id ID) ([]byte, error)
	Set(data []byte) (ID, error)
	Delete(id ID) error
	SetMetadata(id ID, key string, data []byte) error
	GetMetadata(id ID, key string) ([]byte, error)
	DeleteMetadata(id ID, key string) error
}

type fs struct {
	sync.Mutex
	root string
}

const (
	contentDirName  = "content"
	metadataDirName = "metadata"
)

// NewFSStoreBackend returns new filesystem based backend for image.Store
func NewFSStoreBackend(root string) (StoreBackend, error) {
	return newFSStore(root)
}

func newFSStore(root string) (*fs, error) {
	s := &fs{
		root: root,
	}
	if err := os.MkdirAll(filepath.Join(root, contentDirName, string(digest.Canonical)), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirName, string(digest.Canonical)), 0700); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *fs) contentFile(id ID) string {
	dgst := digest.Digest(id)
	return filepath.Join(s.root, contentDirName, string(dgst.Algorithm()), dgst.Hex())
}

func (s *fs) metadataDir(id ID) string {
	dgst := digest.Digest(id)
	return filepath.Join(s.root, metadataDirName, string(dgst.Algorithm()), dgst.Hex())
}

func (s *fs) Walk(f IDWalkFunc) error {
	// Only Canonical digest (sha256) is currently supported
	dir, err := ioutil.ReadDir(filepath.Join(s.root, contentDirName, string(digest.Canonical)))
	if err != nil {
		return err
	}
	for _, v := range dir {
		dgst := digest.NewDigestFromHex(string(digest.Canonical), v.Name())
		if err := dgst.Validate(); err != nil {
			logrus.Debugf("Skipping invalid digest %s: %s", dgst, err)
			continue
		}
		if err := f(ID(dgst)); err != nil {
			return err
		}
	}
	return nil
}

// todo: GetContent?
func (s *fs) Get(id ID) ([]byte, error) {
	s.Lock()
	defer s.Unlock()

	return s.get(id)
}

func (s *fs) get(id ID) ([]byte, error) {
	content, err := ioutil.ReadFile(s.contentFile(id))
	if err != nil {
		return nil, err
	}

	// todo: maybe optional
	validated, err := digest.FromBytes(content)
	if err != nil {
		return nil, err
	}
	if ID(validated) != id {
		return nil, fmt.Errorf("failed to verify image: %v", id)
	}

	return content, nil
}

func (s *fs) Set(data []byte) (ID, error) {
	s.Lock()
	defer s.Unlock()

	if len(data) == 0 {
		return "", fmt.Errorf("Invalid empty data")
	}

	dgst, err := digest.FromBytes(data)
	if err != nil {
		return "", err
	}
	id := ID(dgst)
	filePath := s.contentFile(id)
	tempFilePath := s.contentFile(id) + ".tmp"
	if err := ioutil.WriteFile(tempFilePath, data, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tempFilePath, filePath); err != nil {
		return "", err
	}

	return id, nil
}

// remove base file and helpers
func (s *fs) Delete(id ID) error {
	s.Lock()
	defer s.Unlock()

	if err := os.RemoveAll(s.metadataDir(id)); err != nil {
		return err
	}
	if err := os.Remove(s.contentFile(id)); err != nil {
		return err
	}
	return nil
}

// fails if no base file
func (s *fs) SetMetadata(id ID, key string, data []byte) error {
	s.Lock()
	defer s.Unlock()
	if _, err := s.get(id); err != nil {
		return err
	}

	baseDir := filepath.Join(s.metadataDir(id))
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return err
	}
	filePath := filepath.Join(s.metadataDir(id), key)
	tempFilePath := filePath + ".tmp"
	if err := ioutil.WriteFile(tempFilePath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tempFilePath, filePath)
}

func (s *fs) GetMetadata(id ID, key string) ([]byte, error) {
	s.Lock()
	defer s.Unlock()

	if _, err := s.get(id); err != nil {
		return nil, err
	}
	return ioutil.ReadFile(filepath.Join(s.metadataDir(id), key))
}

func (s *fs) DeleteMetadata(id ID, key string) error {
	s.Lock()
	defer s.Unlock()

	return os.RemoveAll(filepath.Join(s.metadataDir(id), key))
}
