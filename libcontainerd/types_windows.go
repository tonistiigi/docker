package libcontainerd

import "github.com/docker/docker/libcontainerd/windowsoci"

// Spec is the base configuration for the container.
type Spec windowsoci.WindowsSpec

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

// Stats contains a stats properties from containerd.
type Stats struct{} // TODO Windows - what to do with this? containerd.StatsResponse

// Resources defines updatable container resource values.
type Resources struct{}
