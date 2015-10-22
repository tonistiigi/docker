package daemon

import (
	"encoding/json"
	"runtime"
	"strings"
	"time"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/autogen/dockerversion"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/runconfig"
)

// ContainerCommitConfig contains build configs for commit operation,
// and is used when making a commit with the current state of the container.
type ContainerCommitConfig struct {
	Pause   bool
	Repo    string
	Tag     string
	Author  string
	Comment string
	Config  *runconfig.Config
}

// Commit creates a new filesystem image from the current state of a container.
// The image can optionally be tagged into a repository.
func (daemon *Daemon) Commit(container *Container, c *ContainerCommitConfig) (string, error) { // FIXME: change type to images.ID

	if c.Pause && !container.isPaused() {
		container.pause()
		defer container.unpause()
	}

	rwTar, err := container.exportContainerRw()
	if err != nil {
		return "", err
	}
	defer func() {
		if rwTar != nil {
			rwTar.Close()
		}
	}()

	var history []images.History
	var diffIDs []layer.DiffID
	var layerID layer.ID

	if container.ImageID != "" {
		img, err := daemon.imageStore.Get(container.ImageID)
		if err != nil {
			return "", err
		}
		layerID, err = img.GetTopLayerID()
		if err != nil {
			return "", err
		}

		diffIDs = img.RootFS.DiffIDs
		history = img.History
	}

	l, err := daemon.layerStore.Register(rwTar, layerID)
	if err != nil {
		return "", err
	}
	defer daemon.layerStore.Release(l)

	if diffID := l.DiffID(); layer.DigestSha256EmptyTar != diffID {
		diffIDs = append(diffIDs, diffID)
	}

	h := images.History{}
	h.Author = c.Author
	h.Created = time.Now().UTC()
	h.Description = strings.Join(container.Config.Cmd.Slice(), " ")

	if len(c.Comment) > 0 {
		h.Description += "(" + c.Comment + ")"
	}

	history = append(history, h)

	config, err := json.Marshal(&images.Image{
		ImageV1: images.ImageV1{
			DockerVersion:   dockerversion.VERSION,
			Config:          c.Config,
			Architecture:    runtime.GOARCH,
			OS:              runtime.GOOS,
			Container:       container.ID,
			ContainerConfig: *container.Config,
		},
		RootFS: &images.RootFS{
			Type:    "layers",
			DiffIDs: diffIDs,
		},
		History: history,
	})

	if err != nil {
		return "", err
	}

	id, err := daemon.imageStore.Create(config)
	if err != nil {
		return "", err
	}

	if container.ImageID != "" {
		if err := daemon.imageStore.SetParent(id, container.ImageID); err != nil {
			return "", err
		}
	}

	if c.Repo != "" {
		newTag, err := reference.WithName(c.Repo) // todo: should move this to API layer
		if err != nil {
			return "", err
		}
		if c.Tag != "" {
			if newTag, err = reference.WithTag(newTag, c.Tag); err != nil {
				return "", err
			}
		}
		if err := daemon.TagImage(newTag, id.String(), true); err != nil {
			return "", err
		}
	}

	container.logEvent("commit")

	return id.String(), nil

}
