// +build windows

package daemon

import (
	"github.com/docker/docker/container"
	networktypes "github.com/docker/engine-api/types/network"
	"github.com/docker/libnetwork"
)

func (daemon *Daemon) setupLinkedContainers(container *container.Container) ([]string, error) {
	return nil, nil
}

// updateContainerNetworkSettings update the network settings
func (daemon *Daemon) updateContainerNetworkSettings(container *container.Container, endpointsConfig map[string]*networktypes.EndpointSettings) error {
	return nil
}

func (daemon *Daemon) initializeNetworking(container *container.Container) error {
	return nil
}

// ConnectToNetwork connects a container to the network
func (daemon *Daemon) ConnectToNetwork(container *container.Container, idOrName string, endpointConfig *networktypes.EndpointSettings) error {
	return nil
}

// ForceEndpointDelete deletes an endpoing from a network forcefully
func (daemon *Daemon) ForceEndpointDelete(name string, n libnetwork.Network) error {
	return nil
}

// DisconnectFromNetwork disconnects a container from the network.
func (daemon *Daemon) DisconnectFromNetwork(container *container.Container, n libnetwork.Network, force bool) error {
	return nil
}

// getSize returns real size & virtual size
func (daemon *Daemon) getSize(container *container.Container) (int64, int64) {
	// TODO Windows
	return 0, 0
}

// setNetworkNamespaceKey is a no-op on Windows.
func (daemon *Daemon) setNetworkNamespaceKey(containerID string, pid int) error {
	return nil
}

// allocateNetwork is a no-op on Windows.
func (daemon *Daemon) allocateNetwork(container *container.Container) error {
	return nil
}

func (daemon *Daemon) updateNetwork(container *container.Container) error {
	return nil
}

func (daemon *Daemon) releaseNetwork(container *container.Container) {
}

func (daemon *Daemon) setupIpcDirs(container *container.Container) error {
	return nil
}

// TODO Windows: Fix Post-TP4. This is a hack to allow docker cp to work
// against containers which have volumes. You will still be able to cp
// to somewhere on the container drive, but not to any mounted volumes
// inside the container. Without this fix, docker cp is broken to any
// container which has a volume, regardless of where the file is inside the
// container.
func (daemon *Daemon) mountVolumes(container *container.Container) error {
	return nil
}

func detachMounted(path string) error {
	return nil
}

func killProcessDirectly(container *container.Container) error {
	return nil
}
