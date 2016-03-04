package daemon

import (
	"github.com/docker/docker/container"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/libcontainerd/windowsoci"
	"github.com/docker/docker/oci"
)

func (daemon *Daemon) populateCommonSpec(s *windowsoci.Spec, c *container.Container) error {
	linkedEnv, err := daemon.setupLinkedContainers(c)
	if err != nil {
		return err
	}
	s.Root = windowsoci.Root{
		Path:     c.BaseFS,
		Readonly: c.HostConfig.ReadonlyRootfs,
	}
	if err := c.SetupWorkingDirectory(); err != nil {
		return err
	}
	s.Process = windowsoci.Process{
		Args:     append([]string{c.Path}, c.Args...),
		Cwd:      c.Config.WorkingDir,
		Env:      c.CreateDaemonEnvironment(linkedEnv),
		Terminal: c.Config.Tty,
	}
	s.Hostname = c.FullHostname()

	return nil
}

func (daemon *Daemon) createSpec(c *container.Container) (*libcontainerd.Spec, error) {
	s := oci.DefaultSpec()
	if err := daemon.populateCommonSpec(&s.Spec, c); err != nil {
		return nil, err
	}

	//	mounts, err := daemon.setupMounts(c)
	//	if err != nil {
	//		return nil, err
	//	}
	//	if err := setMounts(daemon, &s, c, mounts); err != nil {
	//		return nil, fmt.Errorf("Windows mounts: %v", err)
	//	}

	return (*libcontainerd.Spec)(&s), nil
}

// TODO Windows containerd: This is the old populateCommand which can probably
// be the new start after corrolating with the old exec driver.
//func (daemon *Daemon) populateCommand(c *container.Container, env []string) error {

//	en := &execdriver.Network{
//		Interface: nil,
//	}

//	parts := strings.SplitN(string(c.HostConfig.NetworkMode), ":", 2)
//	switch parts[0] {
//	case "none":
//	case "default", "": // empty string to support existing containers
//		if !c.Config.NetworkDisabled {
//			en.Interface = &execdriver.NetworkInterface{
//				MacAddress:   c.Config.MacAddress,
//				Bridge:       daemon.configStore.bridgeConfig.VirtualSwitchName,
//				PortBindings: c.HostConfig.PortBindings,

//				// TODO Windows. Include IPAddress. There already is a
//				// property IPAddress on execDrive.CommonNetworkInterface,
//				// but there is no CLI option in docker to pass through
//				// an IPAddress on docker run.
//			}
//		}
//	default:
//		return fmt.Errorf("invalid network mode: %s", c.HostConfig.NetworkMode)
//	}

//	// TODO Windows. More resource controls to be implemented later.
//	resources := &execdriver.Resources{
//		CommonResources: execdriver.CommonResources{
//			CPUShares: c.HostConfig.CPUShares,
//		},
//	}

//	processConfig := execdriver.ProcessConfig{
//		CommonProcessConfig: execdriver.CommonProcessConfig{
//			Entrypoint: c.Path,
//			Arguments:  c.Args,
//			Tty:        c.Config.Tty,
//		},
//		ConsoleSize: c.HostConfig.ConsoleSize,
//	}

//	processConfig.Env = env

//	var layerPaths []string
//	img, err := daemon.imageStore.Get(c.ImageID)
//	if err != nil {
//		return fmt.Errorf("Failed to graph.Get on ImageID %s - %s", c.ImageID, err)
//	}

//	if img.RootFS != nil && img.RootFS.Type == "layers+base" {
//		max := len(img.RootFS.DiffIDs)
//		for i := 0; i <= max; i++ {
//			img.RootFS.DiffIDs = img.RootFS.DiffIDs[:i]
//			path, err := layer.GetLayerPath(daemon.layerStore, img.RootFS.ChainID())
//			if err != nil {
//				return fmt.Errorf("Failed to get layer path from graphdriver %s for ImageID %s - %s", daemon.layerStore, img.RootFS.ChainID(), err)
//			}
//			// Reverse order, expecting parent most first
//			layerPaths = append([]string{path}, layerPaths...)
//		}
//	}

//	m, err := c.RWLayer.Metadata()
//	if err != nil {
//		return fmt.Errorf("Failed to get layer metadata - %s", err)
//	}
//	layerFolder := m["dir"]

//	var hvPartition bool
//	// Work out the isolation (whether it is a hypervisor partition)
//	if c.HostConfig.Isolation.IsDefault() {
//		// Not specified by caller. Take daemon default
//		hvPartition = windows.DefaultIsolation.IsHyperV()
//	} else {
//		// Take value specified by caller
//		hvPartition = c.HostConfig.Isolation.IsHyperV()
//	}

//	c.Command = &execdriver.Command{
//		CommonCommand: execdriver.CommonCommand{
//			ID:            c.ID,
//			Rootfs:        c.BaseFS,
//			WorkingDir:    c.Config.WorkingDir,
//			Network:       en,
//			MountLabel:    c.GetMountLabel(),
//			Resources:     resources,
//			ProcessConfig: processConfig,
//			ProcessLabel:  c.GetProcessLabel(),
//		},
//		FirstStart:  !c.HasBeenStartedBefore,
//		LayerFolder: layerFolder,
//		LayerPaths:  layerPaths,
//		Hostname:    c.Config.Hostname,
//		Isolation:   string(c.HostConfig.Isolation),
//		ArgsEscaped: c.Config.ArgsEscaped,
//		HvPartition: hvPartition,
//	}
//	return nil
//}
