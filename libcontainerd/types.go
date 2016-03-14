package libcontainerd

import "io"

// State constants used in state change reporting.
const (
	StateStart        = "start-container"
	StatePause        = "pause"
	StateResume       = "resume"
	StateExit         = "exit"
	StateRestart      = "restart"
	StateRestore      = "restore"
	StateStartProcess = "start-process"
	StateExitProcess  = "exit-process"
	StateOOM          = "oom" // fake state
	stateLive         = "live"
)

// StateInfo contains description about the new state container has entered.
type StateInfo struct { // FIXME: event?
	State     string
	Pid       uint32
	ExitCode  uint32
	ProcessID string
	OOMKilled bool // TODO Windows containerd factor out
}

// IOPipe contains the stdio streams.
type IOPipe struct {
	Stdin    io.WriteCloser
	Stdout   io.Reader
	Stderr   io.Reader
	Terminal bool
}

// Backend defines callbacks that the client of the library needs to implement.
type Backend interface {
	StateChanged(id string, state StateInfo) error
	AttachStreams(id string, io IOPipe) error
}

// Client provides access to containerd features.
type Client interface {
	Create(id string, spec Spec, options ...CreateOption) error
	Signal(id string, sig int) error
	AddProcess(id, processID string, process Process) error
	Resize(id, processID string, width, height int) error
	Pause(id string) error
	Resume(id string) error
	Restore(id string, options ...CreateOption) error
	Stats(id string) (*Stats, error)
	GetPidsForContainer(id string) ([]int, error)
	UpdateResources(id string, resources Resources) error
}

// CreateOption allows to configure parameters of container creation.
type CreateOption interface {
	Apply(interface{}) error
}

// Remote on Linux defines accesspoint to containerd grpc API.
// Remote on Windows is currently unused and only present to have
// consistent interfaces between the daemon and libcontainerd for
// compilation purposes. (TODO Windows containerd)
type Remote interface {
	// Client returns a new Client instance connected with given Backend.
	Client(Backend) (Client, error)
	// Cleanup stops containerd if it was started by libcontainerd.
	Cleanup()
}

// RemoteOption allows to configure paramters of remotes.
type RemoteOption interface {
	Apply(Remote) error
}
