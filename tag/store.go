package tag

import (
	"errors"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/images"
)

// An Association is a tuple associating a reference with an image ID.
type Association struct {
	Ref     reference.Reference
	ImageID images.ID
}

// Store provides the set of methods which can operate on a tag store.
type Store interface {
	References(id images.ID) []reference.Reference
	ReferencesByName(ref reference.Named) []Association
	Add(ref reference.Reference, id images.ID, force bool) error
	Delete(ref reference.Reference) (bool, error)
	Get(ref reference.Reference) (images.ID, error)
}

type store struct {
}

func NewTagStore() Store {
	return &store{}
}

func (store *store) Add(ref reference.Reference, id images.ID, force bool) error {
	return errors.New("not implemented")
}

func (store *store) Delete(ref reference.Reference) (bool, error) {
	return false, errors.New("not implemented")
}

func (store *store) Get(ref reference.Reference) (images.ID, error) {
	return "", errors.New("not implemented")
}

func (store *store) References(id images.ID) []reference.Reference {
	return nil
}

func (store *store) ReferencesByName(ref reference.Named) []Association {
	return nil
}
