package libcontainerd

// process keeps the state for both main container process and exec process.
type process struct {
	client    *client
	id        string // containerID
	processID string
	systemPid uint32
	dir       string
}
