package daemon

import (
	"github.com/docker/docker/container"
	"github.com/opencontainers/specs"
)

func (daemon *Daemon) populateCommonSpec(s *specs.Spec, c *container.Container) error {
	linkedEnv, err := daemon.setupLinkedContainers(c)
	if err != nil {
		return err
	}
	s.Root = specs.Root{
		Path:     c.BaseFS,
		Readonly: c.HostConfig.ReadonlyRootfs,
	}
	if err := c.SetupWorkingDirectory(); err != nil {
		return err
	}
	cwd := c.Config.WorkingDir
	if len(cwd) == 0 {
		cwd = "/"
	}
	s.Process = specs.Process{
		Args:     append([]string{c.Path}, c.Args...),
		Cwd:      cwd,
		Env:      c.CreateDaemonEnvironment(linkedEnv),
		Terminal: c.Config.Tty,
	}
	s.Hostname = c.FullHostname()

	return nil
}
