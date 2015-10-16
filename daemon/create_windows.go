package daemon

import (
	"github.com/docker/docker/images"
	"github.com/docker/docker/runconfig"
)

// createContainerPlatformSpecificSettings performs platform specific container create functionality
func createContainerPlatformSpecificSettings(container *Container, config *runconfig.Config, hostConfig *runconfig.HostConfig, img *images.Image) error {
	return nil
}
