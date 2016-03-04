package windowsoci

// This file is a hack - essentially a mirror of OCI spec for Windows.

import "fmt"

// WindowsSpec is the windows specific configuration for the container.
type WindowsSpec struct {
	Spec

	// Windows is platform specific configuration for Windows based containers.
	Windows Windows `json:"windows"`
}

// Spec is the base configuration for the container.  It specifies platform
// independent configuration. This information must be included when the
// bundle is packaged for distribution.
type Spec struct {

	// Version is the version of the specification that is supported.
	Version string `json:"ociVersion"`
	// Platform is the host information for OS and Arch.
	Platform Platform `json:"platform"`
	// Process is the container's main process.
	Process Process `json:"process"`
	//	// Root is the root information for the container's filesystem.
	Root Root `json:"root"`
	// Hostname is the container's host name.
	Hostname string `json:"hostname,omitempty"`
	// Mounts profile configuration for adding mounts to the container's filesystem.
	Mounts []Mount `json:"mounts"`
	//	// Hooks are the commands run at various lifecycle events of the container.
	//	Hooks Hooks `json:"hooks"`

}

// Windows contains platform specific configuration for Windows based containers.
type Windows struct {
	// Resources contain information for handling resource constraints for the container
	Resources *Resources `json:"resources,omitempty"`
}

// Process contains information to start a specific application inside the container.
type Process struct {
	// Terminal creates an interactive terminal for the container.
	Terminal bool `json:"terminal"`
	//	// User specifies user information for the process.
	//	User User `json:"user"`
	// Args specifies the binary and arguments for the application to execute.
	Args []string `json:"args"`
	// Env populates the process environment for the process.
	Env []string `json:"env,omitempty"`
	// Cwd is the current working directory for the process and must be
	// relative to the container's root.
	Cwd string `json:"cwd"`
}

// Root contains information about the container's root filesystem on the host.
type Root struct {
	// Path is the absolute path to the container's root filesystem.
	Path string `json:"path"`
	// Readonly makes the root filesystem for the container readonly before the process is executed.
	Readonly bool `json:"readonly"`
}

// Platform specifies OS and arch information for the host system that the container
// is created for.
type Platform struct {
	// OS is the operating system.
	OS string `json:"os"`
	// Arch is the architecture
	Arch string `json:"arch"`
}

// Mount specifies a mount for a container.
type Mount struct {
	// Destination is the path where the mount will be placed relative to the container's root.  The path and child directories MUST exist, a runtime MUST NOT create directories automatically to a mount point.
	Destination string `json:"destination"`
	// Type specifies the mount kind.
	Type string `json:"type"`
	// Source specifies the source path of the mount.  In the case of bind mounts
	// this would be the file on the host.
	Source string `json:"source"`
	// Options are fstab style mount options.
	Options []string `json:"options,omitempty"`
}

// Hook specifies a command that is run at a particular event in the lifecycle of a container
type Hook struct {
	//	Path string   `json:"path"`
	//	Args []string `json:"args,omitempty"`
	//	Env  []string `json:"env,omitempty"`
}

// Hooks for container setup and teardown
// TODO Windows containerd: Is this needed?
//type Hooks struct {
//	// Prestart is a list of hooks to be run before the container process is executed.
//	// On Linux, they are run after the container namespaces are created.
//	Prestart []Hook `json:"prestart,omitempty"`
//	// Poststart is a list of hooks to be run after the container process is started.
//	Poststart []Hook `json:"poststart,omitempty"`
//	// Poststop is a list of hooks to be run after the container process exits.
//	Poststop []Hook `json:"poststop,omitempty"`
//}

// Resources has container runtime resource constraints
// TODO Windows containerd. This structure needs ratifying with the old resources
// structure used on Windows and the latest OCI spec.
type Resources struct {
	//	// Memory restriction configuration
	//	Memory *Memory `json:"memory,omitempty"`
	//	// CPU resource restriction configuration
	//	CPU *CPU `json:"cpu,omitempty"`
	//	// Task resource restriction configuration.
	//	Pids *Pids `json:"pids,omitempty"`
	//	// BlockIO restriction configuration
	//	BlockIO *BlockIO `json:"blockIO,omitempty"`
	//	// Network restriction configuration
	//	Network *Network `json:"network,omitempty"`
}

// HACK - below taken from execdriver\driver.go and driver_windows.go

//// Resources contains all resource configs for a driver.
//type Resources struct {
//	Memory            int64  `json:"memory"`
//	MemoryReservation int64  `json:"memory_reservation"`
//	CPUShares         int64  `json:"cpu_shares"`
//	BlkioWeight       uint16 `json:"blkio_weight"`
//}

//// ProcessConfig is the platform specific structure that describes a process
//// that will be run inside a container.
//type ProcessConfig struct {
//	exec.Cmd `json:"-"`
//	Tty        bool     `json:"tty"`
//	Entrypoint string   `json:"entrypoint"`
//	Arguments  []string `json:"arguments"`
//	Terminal   Terminal `json:"-"` // standard or tty terminal
//	ConsoleSize [2]int `json:"-"` // h,w of initial console size
//}

//// Network settings of the container
//type Network struct {
//	MacAddress string `json:"mac"`
//	Bridge     string `json:"bridge"`
//	IPAddress  string `json:"ip"`

//	// PortBindings is the port mapping between the exposed port in the
//	// container and the port on the host.
//	PortBindings nat.PortMap `json:"port_bindings"`

//	ContainerID string            `json:"container_id"` // id of the container to join network.
//}

//// Command wraps an os/exec.Cmd to add more metadata
//type Command struct {
//	ContainerPid  int           `json:"container_pid"` // the pid for the process inside a container
//	ID            string        `json:"id"`
//	MountLabel    string        `json:"mount_label"` // TODO Windows. More involved, but can be factored out
//	Mounts        []Mount       `json:"mounts"`
//	Network       *Network      `json:"network"`
//	ProcessConfig ProcessConfig `json:"process_config"` // Describes the init process of the container.
//	ProcessLabel  string        `json:"process_label"`  // TODO Windows. More involved, but can be factored out
//	Resources     *Resources    `json:"resources"`
//	Rootfs        string        `json:"rootfs"` // root fs of the container
//	WorkingDir    string        `json:"working_dir"`
//	TmpDir        string        `json:"tmpdir"` // Directory used to store docker tmpdirs.
//	FirstStart  bool     `json:"first_start"`  // Optimization for first boot of Windows
//	Hostname    string   `json:"hostname"`     // Windows sets the hostname in the execdriver
//	LayerFolder string   `json:"layer_folder"` // Layer folder for a command
//	LayerPaths  []string `json:"layer_paths"`  // Layer paths for a command
//	Isolation   string   `json:"isolation"`    // Isolation technology for the container
//	ArgsEscaped bool     `json:"args_escaped"` // True if args are already escaped
//	HvPartition bool     `json:"hv_partition"` // True if it's an hypervisor partition
//}

// This is a temporary hack, copied mostly from OCI for Windows support.

const (
	// VersionMajor is for an API incompatible changes
	VersionMajor = 0
	// VersionMinor is for functionality in a backwards-compatible manner
	VersionMinor = 3
	// VersionPatch is for backwards-compatible bug fixes
	VersionPatch = 0

	// VersionDev indicates development branch. Releases will be empty string.
	VersionDev = ""
)

// Version is the specification version that the package types support.
var Version = fmt.Sprintf("%d.%d.%d%s", VersionMajor, VersionMinor, VersionPatch, VersionDev)
