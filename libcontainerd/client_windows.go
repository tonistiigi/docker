package libcontainerd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"syscall"
	"time"

	"github.com/Microsoft/hcsshim"
	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/libcontainerd/windowsoci"
)

type client struct {
	clientCommon

	// Platform specific properties below here (none presently on Windows)
}

// defaultContainerNAT is the default name of the container NAT device that is
// preconfigured on the server. TODO Windows - Remove for TP5 support as not needed.
const defaultContainerNAT = "ContainerNAT"

// Win32 error codes that are used for various workarounds
// These really should be ALL_CAPS to match golangs syscall library and standard
// Win32 error conventions, but golint insists on CamelCase.
const (
	CoEClassstring     = syscall.Errno(0x800401F3) // Invalid class string
	ErrorNoNetwork     = syscall.Errno(1222)       // The network is not present or not started
	ErrorBadPathname   = syscall.Errno(161)        // The specified path is invalid
	ErrorInvalidObject = syscall.Errno(0x800710D8) // The object identifier does not represent a valid object
)

type layer struct {
	ID   string
	Path string
}

type defConfig struct {
	DefFile string
}

type portBinding struct {
	Protocol     string
	InternalPort int
	ExternalPort int
}

type natSettings struct {
	Name         string
	PortBindings []portBinding
}

type networkConnection struct {
	NetworkName string
	//EnableNat bool
	Nat natSettings
}
type networkSettings struct {
	MacAddress string
}

type device struct {
	DeviceType string
	Connection interface{}
	Settings   interface{}
}

type mappedDir struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

type containerInit struct {
	SystemType              string      // HCS requires this to be hard-coded to "Container"
	Name                    string      // Name of the container. We use the docker ID.
	Owner                   string      // The management platform that created this container
	IsDummy                 bool        // Used for development purposes.
	VolumePath              string      // Windows volume path for scratch space
	Devices                 []device    // Devices used by the container
	IgnoreFlushesDuringBoot bool        // Optimization hint for container startup in Windows
	LayerFolderPath         string      // Where the layer folders are located
	Layers                  []layer     // List of storage layers
	ProcessorWeight         uint64      `json:",omitempty"` // CPU Shares 0..10000 on Windows; where 0 will be omitted and HCS will default.
	HostName                string      // Hostname
	MappedDirectories       []mappedDir // List of mapped directories (volumes/mounts)
	SandboxPath             string      // Location of unmounted sandbox (used for Hyper-V containers, not Windows Server containers)
	HvPartition             bool        // True if it a Hyper-V Container
	EndpointList            []string    //List of endpoints to be attached to container
}

// defaultOwner is a tag passed to HCS to allow it to differentiate between
// container creator management stacks. We hard code "docker" in the case
// of docker.
const defaultOwner = "docker"

func (clnt *client) Create(containerID string, spec Spec, options ...CreateOption) error {
	logrus.Debugln("LCD client.Create() with spec", spec)

	cu := &containerInit{
		SystemType: "Container",
		Name:       containerID,
		Owner:      defaultOwner,

		VolumePath:              spec.Root.Path,
		IgnoreFlushesDuringBoot: spec.Windows.FirstStart,
		LayerFolderPath:         spec.Windows.LayerFolder,
		HostName:                spec.Hostname,
	}

	if spec.Windows.Networking != nil {
		cu.EndpointList = spec.Windows.Networking.EndpointList
	}

	if spec.Windows.Resources != nil && spec.Windows.Resources.CPU != nil {
		cu.ProcessorWeight = *spec.Windows.Resources.CPU.Shares
	}

	if spec.Windows.HvRuntime != nil {
		cu.HvPartition = len(spec.Windows.HvRuntime.ImagePath) > 0
	}

	if cu.HvPartition {
		cu.SandboxPath = filepath.Dir(spec.Windows.LayerFolder)
	} else {
		cu.VolumePath = spec.Root.Path
		cu.LayerFolderPath = spec.Windows.LayerFolder
	}

	for _, layerPath := range spec.Windows.LayerPaths {
		_, filename := filepath.Split(layerPath)
		g, err := hcsshim.NameToGuid(filename)
		if err != nil {
			return err
		}
		cu.Layers = append(cu.Layers, layer{
			ID:   g.ToString(),
			Path: layerPath,
		})
	}

	// Add the mounts (volumes, bind mounts etc) to the structure
	mds := make([]mappedDir, len(spec.Mounts))
	for i, mount := range spec.Mounts {
		mds[i] = mappedDir{
			HostPath:      mount.Source,
			ContainerPath: mount.Destination,
			ReadOnly:      mount.Readonly}
	}
	cu.MappedDirectories = mds

	// TODO Windows: vv START OF TP4 BLOCK OF CODE. REMOVE ONCE TP4 IS NO LONGER SUPPORTED
	if hcsshim.IsTP4() && spec.Windows.Networking != nil {
		// Enumerate through the port bindings specified by the user and convert
		// them into the internal structure matching the JSON blob that can be
		// understood by the HCS.
		var pbs []portBinding
		for i, v := range spec.Windows.Networking.PortBindings {
			proto := strings.ToUpper(i.Proto())
			if proto != "TCP" && proto != "UDP" {
				return fmt.Errorf("invalid protocol %s", i.Proto())
			}

			if len(v) > 1 {
				return fmt.Errorf("Windows does not support more than one host port in NAT settings")
			}

			for _, v2 := range v {
				var (
					iPort, ePort int
					err          error
				)
				if len(v2.HostIP) != 0 {
					return fmt.Errorf("Windows does not support host IP addresses in NAT settings")
				}
				if ePort, err = strconv.Atoi(v2.HostPort); err != nil {
					return fmt.Errorf("invalid container port %s: %s", v2.HostPort, err)
				}
				if iPort, err = strconv.Atoi(i.Port()); err != nil {
					return fmt.Errorf("invalid internal port %s: %s", i.Port(), err)
				}
				if iPort < 0 || iPort > 65535 || ePort < 0 || ePort > 65535 {
					return fmt.Errorf("specified NAT port is not in allowed range")
				}
				pbs = append(pbs,
					portBinding{ExternalPort: ePort,
						InternalPort: iPort,
						Protocol:     proto})
			}
		}

		dev := device{
			DeviceType: "Network",
			Connection: &networkConnection{
				NetworkName: spec.Windows.Networking.Bridge,
				Nat: natSettings{
					Name:         defaultContainerNAT,
					PortBindings: pbs,
				},
			},
		}

		if spec.Windows.Networking.MacAddress != "" {
			windowsStyleMAC := strings.Replace(
				spec.Windows.Networking.MacAddress, ":", "-", -1)
			dev.Settings = networkSettings{
				MacAddress: windowsStyleMAC,
			}
		}
		cu.Devices = append(cu.Devices, dev)
	} else {
		logrus.Debugln("No network interface")
	}
	// TODO Windows: ^^ END OF TP4 BLOCK OF CODE. REMOVE ONCE TP4 IS NO LONGER SUPPORTED

	configurationb, err := json.Marshal(cu)
	if err != nil {
		return err
	}

	configuration := string(configurationb)

	// TODO Windows TP5 timeframe. Remove when TP4 is no longer supported.
	// The following a workaround for Windows TP4 which has a networking
	// bug which fairly frequently returns an error. Back off and retry.
	if !hcsshim.IsTP4() {
		if err := hcsshim.CreateComputeSystem(containerID, configuration); err != nil {
			return err
		}
	} else {
		maxAttempts := 5
		for i := 0; i < maxAttempts; i++ {
			err = hcsshim.CreateComputeSystem(containerID, configuration)
			if err == nil {
				break
			}

			if herr, ok := err.(*hcsshim.HcsError); ok {
				if herr.Err != syscall.ERROR_NOT_FOUND && // Element not found
					herr.Err != syscall.ERROR_FILE_NOT_FOUND && // The system cannot find the file specified
					herr.Err != ErrorNoNetwork && // The network is not present or not started
					herr.Err != ErrorBadPathname && // The specified path is invalid
					herr.Err != CoEClassstring && // Invalid class string
					herr.Err != ErrorInvalidObject { // The object identifier does not represent a valid object
					logrus.Debugln("Failed to create temporary container ", err)
					return err
				}
				logrus.Warnf("Invoking Windows TP4 retry hack (%d of %d)", i, maxAttempts-1)
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	container := clnt.newContainer(containerID, spec.Process, options...)

	defer func() {
		if err != nil {
			clnt.deleteContainer(containerID)
		}
	}()

	logrus.Debugf("Finished Create() id=%s, calling container.start()", containerID)
	return container.start()
}

// TODO Implement
func (clnt *client) AddProcess(containerID, processID string, specp Process) error {
	return errors.New("Not yet implemented on Windows")
}

func (clnt *client) Signal(containerID string, sig int) error {
	var (
		cont *container
		err  error
	)

	// Get the container as we need it to find the pid of the process.
	clnt.lock(containerID)
	defer clnt.unlock(containerID)
	if cont, err = clnt.getContainer(containerID); err != nil {
		return err
	}

	logrus.Debugf("lcd: Signal() containerID=%s sig=%d pid=%d", containerID, sig, cont.systemPid)
	context := fmt.Sprintf("Signal: sig=%d pid=%d", sig, cont.systemPid)

	if syscall.Signal(sig) == syscall.SIGKILL {
		// Terminate the compute system
		if err := hcsshim.TerminateComputeSystem(containerID, hcsshim.TimeoutInfinite, context); err != nil {
			logrus.Errorf("Failed to terminate %s - %q", containerID, err)
		}

	} else {
		// Terminate Process
		if err = hcsshim.TerminateProcessInComputeSystem(containerID, cont.systemPid); err != nil {
			logrus.Warnf("Failed to terminate pid %d in %s: %q", cont.systemPid, containerID, err)
			// Ignore errors
			err = nil
		}

		// Shutdown the compute system
		if err := hcsshim.ShutdownComputeSystem(containerID, hcsshim.TimeoutInfinite, context); err != nil {
			logrus.Errorf("Failed to shutdown %s - %q", containerID, err)
		}
	}
	return nil
}

func (clnt *client) Resize(containerID, processFriendlyName string, width, height int) error {
	// Get the libcontainerd container object
	clnt.lock(containerID)
	defer clnt.unlock(containerID)
	cont, err := clnt.getContainer(containerID)
	if err != nil {
		return err
	}

	if processFriendlyName == InitFriendlyName {
		logrus.Debugln("Resizing systemPID in", containerID, cont.process.systemPid)
		return hcsshim.ResizeConsoleInComputeSystem(containerID, cont.process.systemPid, height, width)
	}

	// TODO Windows containerd.
	// A. All this looking up is extremely inefficient. Old code passed the PID in directly
	// B. This needs revisiting for exec'd processes. Currently only sending the systemPID
	//	for _, p := range cont.processes {
	//		if p.friendlyName == processFriendlyName {
	//			//if p.ociProcess.Terminal {
	//			logrus.Debugln("Resizing exec'd process", containerID, p.systemPid)
	//			return hcsshim.ResizeConsoleInComputeSystem(containerID, p.systemPid, height, width)
	//			//}
	//		}
	//	}

	return err

}

func (clnt *client) Pause(containerID string) error {
	return errors.New("Windows: Containers cannot be paused")
}

func (clnt *client) Resume(containerID string) error {
	return errors.New("Windows: Containers cannot be paused")
}

func (clnt *client) Stats(containerID string) (*Stats, error) {
	return nil, errors.New("Windows: Stats not implemented")
}

// TODO Implement
func (clnt *client) Restore(containerID string, options ...CreateOption) error {
	return errors.New("Not yet implemented on Windows")
}

func (clnt *client) GetPidsForContainer(containerID string) ([]int, error) {
	return nil, errors.New("GetPidsForContainer: GetPidsForContainer() not implemented")
}

func (clnt *client) UpdateResources(containerID string, resources Resources) error {
	// Updating resource isn't supported on Windows
	// but we should return nil for enabling updating container
	return nil
}

func (clnt *client) newContainer(containerID string, p windowsoci.Process, options ...CreateOption) *container {
	container := &container{
		containerCommon: containerCommon{
			process: process{
				processCommon: processCommon{
					containerID:  containerID,
					client:       clnt,
					friendlyName: InitFriendlyName,
				},
				ociProcess: p,
			},
			processes: make(map[string]*process),
		},
	}

	// BUGBUG TODO Windows containerd. What options?
	//	for _, option := range options {
	//		if err := option.Apply(container); err != nil {
	//			logrus.Error(err)
	//		}
	//	}

	return container
}
