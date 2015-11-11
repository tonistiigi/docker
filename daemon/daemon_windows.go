package daemon

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/image"
	"github.com/docker/docker/tag"
	// register the windows graph driver
	"github.com/docker/docker/daemon/graphdriver/windows"
	"github.com/docker/docker/pkg/parsers"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/docker/runconfig"
	"github.com/docker/libnetwork"
	blkiodev "github.com/opencontainers/runc/libcontainer/configs"
)

const (
	defaultVirtualSwitch = "Virtual Switch"
	platformSupported    = true
	windowsMinCPUShares  = 1
	windowsMaxCPUShares  = 10000
)

func getBlkioWeightDevices(config *runconfig.HostConfig) ([]*blkiodev.WeightDevice, error) {
	return nil, nil
}

func parseSecurityOpt(container *Container, config *runconfig.HostConfig) error {
	return nil
}

func setupInitLayer(initLayer string, rootUID, rootGID int) error {
	return nil
}

func checkKernel() error {
	return nil
}

// adaptContainerSettings is called during container creation to modify any
// settings necessary in the HostConfig structure.
func (daemon *Daemon) adaptContainerSettings(hostConfig *runconfig.HostConfig, adjustCPUShares bool) {
	if hostConfig == nil {
		return
	}

	if hostConfig.CPUShares < 0 {
		logrus.Warnf("Changing requested CPUShares of %d to minimum allowed of %d", hostConfig.CPUShares, windowsMinCPUShares)
		hostConfig.CPUShares = windowsMinCPUShares
	} else if hostConfig.CPUShares > windowsMaxCPUShares {
		logrus.Warnf("Changing requested CPUShares of %d to maximum allowed of %d", hostConfig.CPUShares, windowsMaxCPUShares)
		hostConfig.CPUShares = windowsMaxCPUShares
	}
}

// verifyPlatformContainerSettings performs platform-specific validation of the
// hostconfig and config structures.
func verifyPlatformContainerSettings(daemon *Daemon, hostConfig *runconfig.HostConfig, config *runconfig.Config) ([]string, error) {
	return nil, nil
}

// checkConfigOptions checks for mutually incompatible config options
func checkConfigOptions(config *Config) error {
	return nil
}

// checkSystem validates platform-specific requirements
func checkSystem() error {
	// Validate the OS version. Note that docker.exe must be manifested for this
	// call to return the correct version.
	osv, err := system.GetOSVersion()
	if err != nil {
		return err
	}
	if osv.MajorVersion < 10 {
		return fmt.Errorf("This version of Windows does not support the docker daemon")
	}
	if osv.Build < 10586 {
		return fmt.Errorf("The Windows daemon requires Windows Server 2016 Technical Preview 4, build 10586 or later")
	}
	return nil
}

// configureKernelSecuritySupport configures and validate security support for the kernel
func configureKernelSecuritySupport(config *Config, driverName string) error {
	return nil
}

func migrateIfDownlevel(driver graphdriver.Driver, root string) error {
	return nil
}

func isBridgeNetworkDisabled(config *Config) bool {
	return false
}

func (daemon *Daemon) initNetworkController(config *Config) (libnetwork.NetworkController, error) {
	// Set the name of the virtual switch if not specified by -b on daemon start
	if config.Bridge.VirtualSwitchName == "" {
		config.Bridge.VirtualSwitchName = defaultVirtualSwitch
	}
	return nil, nil
}

// registerLinks sets up links between containers and writes the
// configuration out for persistence.
func (daemon *Daemon) registerLinks(container *Container, hostConfig *runconfig.HostConfig) error {
	// TODO Windows. Factored out for network modes. There may be more
	// refactoring required here.

	if hostConfig == nil || hostConfig.Links == nil {
		return nil
	}

	for _, l := range hostConfig.Links {
		name, alias, err := parsers.ParseLink(l)
		if err != nil {
			return err
		}
		child, err := daemon.Get(name)
		if err != nil {
			//An error from daemon.Get() means this name could not be found
			return fmt.Errorf("Could not get container for %s", name)
		}
		if err := daemon.registerLink(container, child, alias); err != nil {
			return err
		}
	}

	// After we load all the links into the daemon
	// set them to nil on the hostconfig
	hostConfig.Links = nil
	if err := container.writeHostConfig(); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) newBaseContainer(id string) *Container {
	return &Container{
		CommonContainer: CommonContainer{
			ID:           id,
			State:        NewState(),
			execCommands: newExecStore(),
			root:         daemon.containerRoot(id),
		},
	}
}

func (daemon *Daemon) cleanupMounts() error {
	return nil
}

// conditionalMountOnStart is a platform specific helper function during the
// container start to call mount.
func (daemon *Daemon) conditionalMountOnStart(container *Container) error {
	// We do not mount if a Hyper-V container
	if !container.hostConfig.Isolation.IsHyperV() {
		if err := daemon.Mount(container); err != nil {
			return err
		}
	}
	return nil
}

// conditionalUnmountOnCleanup is a platform specific helper function called
// during the cleanup of a container to unmount.
func (daemon *Daemon) conditionalUnmountOnCleanup(container *Container) {
	// We do not unmount if a Hyper-V container
	if !container.hostConfig.Isolation.IsHyperV() {
		daemon.Unmount(container)
	}
}

func restoreCustomImage(driver graphdriver.Driver, is image.Store, ts tag.Store) error {
	if wd, ok := driver.(*windows.Driver); ok {
		imageInfos, err := wd.GetCustomImageInfos()
		if err != nil {
			return err
		}

		// Convert imageData to valid image configuration
		for i := range imageInfos {
			name := imageInfos[i].Name
			path := imageInfos[i].Path

			// Create history for base layer
			config, err := json.Marshal(&image.Image{
				V1Image: image.V1Image{
					DockerVersion: dockerversion.Version,
					Architecture:  runtime.GOARCH,
					OS:            runtime.GOOS,
					Created:       imageInfos[i].CreatedTime,
				},
				RootFS: &image.RootFS{
					// TODO: Create custom type which keeps reference to windows base layer
					Type: "layers",
				},
				History: []image.History{
					{
						Created:    imageInfos[i].CreatedTime,
						Comment:    fmt.Sprintf("Base image %s at %s", name, path),
						EmptyLayer: true,
					},
				},
			})

			named, err := reference.ParseNamed(name)
			if err != nil {
				return err
			}

			ref, err := reference.WithTag(named, imageInfos[i].Version)
			if err != nil {
				return err
			}

			id, err := is.Create(config)
			if err != nil {
				return err
			}

			if err := ts.Add(ref, id, true); err != nil {
				return err
			}

			logrus.Debugf("Registered base layer %s as %s", ref, id)
		}

	}

	return nil
}
