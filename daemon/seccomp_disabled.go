// +build !seccomp,!windows

package daemon

import (
	"github.com/docker/docker/container"
	"github.com/opencontainers/specs/specs-go"
)

func setSeccomp(daemon *Daemon, rs *specs.LinuxRuntimeSpec, c *container.Container) error {
	return nil
}
