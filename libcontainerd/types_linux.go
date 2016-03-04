package libcontainerd

import (
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/opencontainers/specs"
)

type Spec specs.LinuxSpec

type Process struct {
	// Terminal creates an interactive terminal for the container.
	Terminal bool `json:"terminal"`
	// User specifies user information for the process.
	User *User `json:"user"`
	// Args specifies the binary and arguments for the application to execute.
	Args []string `json:"args"`
	// Env populates the process environment for the process.
	Env []string `json:"env,omitempty"`
	// Cwd is the current working directory for the process and must be
	// relative to the container's root.
	Cwd *string `json:"cwd"`
	// Capabilities are linux capabilities that are kept for the container.
	// Capabilities []string `json:"capabilities,omitempty"`
	// // ApparmorProfile specified the apparmor profile for the container.
	// ApparmorProfile *string `json:"apparmorProfile,omitempty"`
	// // SelinuxProcessLabel specifies the selinux context that the container process is run as.
	// SelinuxLabel *string `json:"selinuxLabel,omitempty"`
}

type Stats containerd.StatsResponse

type User specs.User
