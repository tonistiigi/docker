package daemon

import (
	"runtime"
	"strings"
	"time"

	"github.com/docker/docker/autogen/dockerversion"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/runconfig"
	"github.com/jfrazelle/go/canonical/json"
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

	img, err := daemon.imageStore.Get(container.ImageID)
	if err != nil {
		return "", err
	}
	layerID, err := img.GetTopLayerID()
	if err != nil {
		return "", err
	}
	l, err := daemon.layerStore.Register(rwTar, layerID)
	if err != nil {
		return "", err
	}
	defer daemon.layerStore.Release(l)

	diffIDs := img.RootFS.DiffIDs
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

	history := append(img.History, h)

	config, err := json.MarshalCanonical(&images.Image{
		ImageV1: images.ImageV1{
			DockerVersion:   dockerversion.VERSION,
			Config:          container.Config,
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

	container.logEvent("commit")

	// FIXME: tagging

	return id.String(), nil

	// // Register the image if needed
	// if c.Repo != "" {
	// 	if err := daemon.repositories.Tag(c.Repo, c.Tag, img.ID, true); err != nil {
	// 		return img, err
	// 	}
	// }
	// container.logEvent("commit")
	// return img, nil
}
