package metadata

import (
	"github.com/docker/docker/images"
)

// V1IDService maps v1 IDs to images on disk.
type V1IDService struct {
	store Store
}

// NewV1IDService creates a new V1 ID mapping service.
func NewV1IDService(store Store) *V1IDService {
	return &V1IDService{
		store: store,
	}
}

// namespace returns the namespace used by this service.
func (idserv *V1IDService) namespace() string {
	return "v1id"
}

// Get finds an image by its V1 ID.
func (idserv *V1IDService) Get(v1ID string) (images.ID, error) {
	idBytes, err := idserv.store.Get(idserv.namespace(), v1ID)
	if err != nil {
		return images.ID(""), err
	}
	return images.ID(idBytes), nil
}

// Set associates an image with a V1 ID.
func (idserv *V1IDService) Set(v1ID string, id images.ID) error {
	return idserv.store.Set(idserv.namespace(), v1ID, []byte(id))
}
