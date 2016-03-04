package libcontainerd

import (
//	"github.com/docker/docker/libcontainerd/windowsoci"
)

// TODO Implement
func (c *client) AddProcess(id, processID string, specp Process) error {
	return nil
}

// TODO Implement
func (c *client) Create(id string, spec Spec, options ...CreateOption) (err error) {
	return nil
}

// TODO Implment
func (c *client) Signal(id string, sig int) error {
	return nil
}

// TODO Implement
//func (c *client) restore(cont *containerd.Container, options ...CreateOption) (err error) {
//	return nil
//}

// TODO Implement
func (c *client) Resize(id, processID string, width, height int) error {
	return nil
}

// TODO Implement (error on Windows)
func (c *client) Pause(id string) error {
	return nil
}

//// TODO Implement
//func (c *client) setState(id, state string) error {
//	return nil
//}

// TODO Implement
func (c *client) Resume(id string) error {
	return nil
}

// TODO Implement (error on Windows for now)
func (c *client) Stats(id string) (*Stats, error) {
	return nil, nil
}

// TODO Implement
func (c *client) Restore(id string, options ...CreateOption) error {
	return nil
}

// TODO Implement
func (c *client) GetPidsForContainer(id string) ([]int, error) {
	return nil, nil
}

//func (c *client) getContainerdContainer(id string) (*containerd.Container, error) {
//	return nil, nil
//}

//func (c *client) newContainer(dir string, options ...CreateOption) *container {
//	container := &container{}
//	return container
//}

//func (c *client) getContainer(id string) (*container, error) {
//	return nil, nil
//}
