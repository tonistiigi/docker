package libcontainerd

import (
	//	"github.com/Sirupsen/logrus"
	"sync"

	"github.com/Sirupsen/logrus"
)

type remote struct {
	sync.RWMutex
	stateDir string
	clients  []*client
}

// TODO JJH - Still sure this can be entirely factored out on Windows.
// Need to play some more with the code.

// New creates a fresh instance of libcontainerd remote.
func New(stateDir string, options ...RemoteOption) (Remote, error) {
	logrus.Debugf("libcontainerd remote new() in stateDir %v", stateDir)
	r := &remote{
		stateDir: stateDir,
	}
	return r, nil
}

// TODO Windows containerd. To implement
func (r *remote) Cleanup() {
}

// setClientPlatformFields sets up platform specific fields in a client
// structure. This is a no-op on Windows
func (r *remote) setClientPlatformFields(client *client) {
}
