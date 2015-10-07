package tag

import (
	"errors"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/images"
)

type Walker func(ref reference.Reference, imageID images.ID) error

type Store interface {
	Walk(walker Walker) error
	Delete(ref reference.Reference) (bool, error)
	Get(ref reference.Reference) (images.ID, error)
	GetRepoTags(repoName string) []reference.Reference
	Tag(id images.ID, ref reference.Reference, force bool) error
}

type store struct {
}

func NewTagStore() Store {
	return &store{}
}

func (store *store) Walk(walker Walker) error {
	return errors.New("not implemented")
}

func (store *store) Delete(ref reference.Reference) (bool, error) {
	return false, errors.New("not implemented")
}

func (store *store) Get(ref reference.Reference) (images.ID, error) {
	return "", errors.New("not implemented")
}

func (store *store) GetRepoTags(repoName string) []reference.Reference {
	return nil
}

func (store *store) Tag(id images.ID, ref reference.Reference, force bool) error {
	return errors.New("not implemented")
}
