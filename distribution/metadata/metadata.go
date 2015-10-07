package metadata

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
)

// Store implements a K/V store for mapping distribution-related IDs
// to on-disk layer IDs and image IDs. The namespace identifies the type of
// mapping (i.e. "v1ids" or "artifacts"). MetadataStore is goroutine-safe.
type Store interface {
	// Get retrieves data by namespace and key.
	Get(namespace string, key string) ([]byte, error)
	// Set writes data indexed by namespace and key.
	Set(namespace, key string, value []byte) error
}

// FSMetadataStore uses the filesystem to associate metadata with layer and
// image IDs.
type FSMetadataStore struct {
	sync.RWMutex
	basePath string
}

// NewFSMetadataStore creates a new filesystem-based metadata store.
func NewFSMetadataStore(basePath string) *FSMetadataStore {
	return &FSMetadataStore{
		basePath: basePath,
	}
}

func (store *FSMetadataStore) path(namespace, key string) string {
	return filepath.Join(store.basePath, namespace, key)
}

// Get retrieves data by namespace and key. The data is read from a file named
// after the key, stored in the namespace's directory.
func (store *FSMetadataStore) Get(namespace string, key string) ([]byte, error) {
	store.RLock()
	defer store.RUnlock()

	return ioutil.ReadFile(store.path(namespace, key))
}

// Set writes data indexed by namespace and key. The data is written to a file
// named after the key, stored in the namespace's directory.
func (store *FSMetadataStore) Set(namespace, key string, value []byte) error {
	store.Lock()
	defer store.Unlock()

	if err := os.MkdirAll(filepath.Join(store.basePath, namespace), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(store.path(namespace, key), value, 0644)
}
