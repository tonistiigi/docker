package swarm

// StackCreateResponse contains the information returned to a client on the
// creation of a new stack.
type StackCreateResponse struct {
	ServiceIDs []string
}
