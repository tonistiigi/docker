package daemon

import (
	"path/filepath"
	"runtime"

	"github.com/docker/docker/container"
	"github.com/opencontainers/specs"
)

func initSpec(c *container.Container, env []string) specs.LinuxSpec {
	cspec := defaultTemplate
	cspec.Version = specs.Version
	cspec.Platform = specs.Platform{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	cspec.Root = specs.Root{
		Path:     "rootfs",
		Readonly: c.HostConfig.ReadonlyRootfs,
	}
	cwd := c.Config.WorkingDir
	if len(cwd) == 0 {
		cwd = "/"
	}
	cspec.Process = specs.Process{
		Args:     append([]string{c.Path}, c.Args...),
		Cwd:      cwd,
		Env:      env,
		Terminal: c.Config.Tty,
	}
	cspec.Hostname = c.Config.Hostname

	var cgroupsPath string
	if c.HostConfig.CgroupParent != "" {
		cgroupsPath = filepath.Join(c.HostConfig.CgroupParent, c.ID)
	} else {
		// TODO: Detect systemd?
		cgroupsPath = filepath.Join("/docker", c.ID)
	}
	cspec.Linux.CgroupsPath = &cgroupsPath

	return cspec
}
