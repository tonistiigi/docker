package daemon

import (
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/exec"
	"github.com/docker/docker/libcontainerd"
)

// No-op on Windows
func execSetUser(c *container.Container, ec *exec.Config, p *libcontainerd.Process) error {
	return nil
}
