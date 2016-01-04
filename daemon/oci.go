package daemon

import (
	"runtime"

	"github.com/docker/docker/container"
	"github.com/opencontainers/specs"
)

type combinedSpec struct {
	spec  specs.Spec
	rspec specs.RuntimeSpec
}

func initSpec(c *container.Container, env []string) combinedSpec {
	cspec := defaultTemplate
	cspec.spec.Version = specs.Version
	cspec.spec.Platform = specs.Platform{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	cspec.spec.Root = specs.Root{
		Path:     "rootfs",
		Readonly: c.HostConfig.ReadonlyRootfs,
	}
	cspec.spec.Process = specs.Process{
		Args:     append([]string{c.Path}, c.Args...),
		Cwd:      c.Config.WorkingDir,
		Env:      env,
		Terminal: c.Config.Tty,
	}
	cspec.spec.Hostname = c.Config.Hostname
	return cspec
}
