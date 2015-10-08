package images

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/docker/distribution/digest"
)

// FIXME: unit-test this

// IDWalKFunc is function called by StoreBackgend.Walk
type IDWalKFunc func(id digest.Digest) error

// StoreBackend provides interface for image.Store persistence
type StoreBackend interface {
	Walk(f IDWalKFunc) error
	Get(id digest.Digest) ([]byte, error)
	Set(data []byte) (digest.Digest, error)
	Delete(id digest.Digest) error
	SetMetadata(id digest.Digest, key string, data []byte) error
	GetMetadata(id digest.Digest, key string) ([]byte, error)
}

type fs struct {
	sync.Mutex
	root string
}

const (
	contentDirName  = "content"
	metadataDirName = "metadata"
)

func newFSStore(root string) (*fs, error) {
	s := &fs{
		root: root,
	}
	if err := os.MkdirAll(filepath.Join(root, contentDirName), 0600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirName), 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *fs) Walk(f IDWalKFunc) error {
	dir, err := ioutil.ReadDir(filepath.Join(s.root, contentDirName))
	if err != nil {
		return err
	}
	for _, v := range dir {
		dgst := digest.Digest(v.Name())
		if err := validateCanonicalDigest(dgst); err != nil {
			// todo: log error
			continue
		}
		if err := f(dgst); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonicalDigest(dgst digest.Digest) error {
	if err := dgst.Validate(); err != nil {
		return nil
	}
	if dgst.Algorithm() != digest.Canonical {
		return fmt.Errorf("unsupported digest algorithm: %v", dgst.Algorithm())
	}
	return nil
}

// todo: GetContent?
func (s *fs) Get(id digest.Digest) ([]byte, error) {
	s.Lock()
	defer s.Unlock()

	return s.get(id)
}

func (s *fs) get(id digest.Digest) ([]byte, error) {
	if err := validateCanonicalDigest(id); err != nil {
		return nil, err
	}

	content, err := ioutil.ReadFile(filepath.Join(s.root, contentDirName, id.String()))
	if err != nil {
		return nil, err
	}

	// todo: maybe optional
	validated, err := digest.FromBytes(content)
	if err != nil {
		return nil, err
	}
	if validated != id {
		return nil, fmt.Errorf("failed to verify image: %v", id)
	}

	return content, nil
}

func (s *fs) Set(data []byte) (digest.Digest, error) {
	s.Lock()
	defer s.Unlock()

	dgst, err := digest.FromBytes(data)
	if err != nil {
		return "", err
	}

	if err := ioutil.WriteFile(filepath.Join(s.root, contentDirName, dgst.String()), data, 0600); err != nil {
		return "", err
	}

	return dgst, nil
}

// remove base file and helpers
func (s *fs) Delete(id digest.Digest) error {
	s.Lock()
	defer s.Unlock()

	if err := os.RemoveAll(filepath.Join(s.root, metadataDirName, id.String())); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.root, contentDirName, id.String())); err != nil {
		return err
	}
	return nil
}

// fails if no base file
func (s *fs) SetMetadata(id digest.Digest, key string, data []byte) error {
	s.Lock()
	defer s.Unlock()
	if _, err := s.get(id); err != nil {
		return err
	}

	baseDir := filepath.Join(s.root, metadataDirName, string(id))
	if err := os.MkdirAll(baseDir, 0600); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(baseDir, key), data, 0600); err != nil {
		return err
	}

	return nil
}

func (s *fs) GetMetadata(id digest.Digest, key string) ([]byte, error) {
	s.Lock()
	defer s.Unlock()

	if _, err := s.get(id); err != nil {
		return nil, err
	}
	return ioutil.ReadFile(filepath.Join(s.root, metadataDirName, id.String()))
}
