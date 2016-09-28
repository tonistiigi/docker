package stack

import types "github.com/docker/docker/api/types/swarm"

// Backend is all the methods that need to be implemented
// to provide stack specific functionality.
type Backend interface {
	CreateStack(name, bundle, encodedAuth string) (*types.StackCreateResponse, error) // TODO: add config
}
