package libcontainerd

import "sync"

// Not used on Windows. Only present for compilation.
type remote struct {
	sync.RWMutex
	clients []*client
}

// New creates a fresh instance of libcontainerd remote.
// TODO Windows containerd. To implement.
func New(stateDir string, options ...RemoteOption) (Remote, error) {
	r := &remote{}
	return r, nil
}

// TODO Windows containerd. To implement
func (r *remote) Cleanup() {
}

// TODO Windows containerd. Implement me
// BUGBUG If neither Windows/Linux return nil, no need for error...
func (r *remote) Client(b Backend) (Client, error) {
	c := &client{
		backend:          b,
		remote:           r,
		containers:       make(map[string]*container),
		containerMutexes: make(map[string]*sync.Mutex),
	}

	r.Lock()
	r.clients = append(r.clients, c)
	r.Unlock()
	return c, nil
}
