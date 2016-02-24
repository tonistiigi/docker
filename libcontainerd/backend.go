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
)

// Backend defines callbacks that the client of the library needs to implement.
type Backend interface {
	StateChanged(id string, state StateInfo) error
	AttachStreams(id string, io IOPipe) error
}

// IOPipe contains the stdio streams.
type IOPipe struct {
	Stdin    io.WriteCloser
	Stdout   io.Reader
	Stderr   io.Reader
	Terminal bool
}

// StateInfo contains description about the new state container has entered.
type StateInfo struct { // FIXME: event?
	State     string
	Pid       uint32
	ExitCode  uint32
	ProcessID string
	OOMKilled bool
}
