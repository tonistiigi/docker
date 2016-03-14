package libcontainerd

// processCommon are the platform common fields as part of the process structure
// which keeps the state for the main container process, as well as any exec
// processes.
type processCommon struct {
	client *client

	// id is the Container ID
	id string

	// friendlyName is an identifier for the process (or `initFriendlyName`
	// for the first process)
	friendlyName string

	// systemPid is the PID of the main container process
	systemPid uint32
}
