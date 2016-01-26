// +build !seccomp

package daemon

import (
	"github.com/docker/docker/container"
	"github.com/opencontainers/specs"
)

func setSeccomp(daemon *Daemon, rs *specs.LinuxRuntimeSpec, c *container.Container) error {
	return nil
}
