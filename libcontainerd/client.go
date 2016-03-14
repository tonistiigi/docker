package libcontainerd

import (
	"fmt"
	"sync"

	"github.com/Sirupsen/logrus"
)

// clientCommon contains the platform agnostic fields used in the client structure
type clientCommon struct {
	backend          Backend
	containers       map[string]*container
	containerMutexes map[string]*sync.Mutex // lock by container ID
	mapMutex         sync.RWMutex           // protects read/write oprations from containers map
	sync.Mutex                              // lock for containerMutexes map access
}

func (c *client) lock(id string) {
	c.Lock()
	if _, ok := c.containerMutexes[id]; !ok {
		c.containerMutexes[id] = &sync.Mutex{}
	}
	c.Unlock()
	c.containerMutexes[id].Lock()
}

func (c *client) unlock(id string) {
	c.Lock()
	if l, ok := c.containerMutexes[id]; ok {
		l.Unlock()
	} else {
		logrus.Warnf("unlock of non-existing mutex: %s", id)
	}
	c.Unlock()
}

// must hold a lock for c.ID
func (c *client) appendContainer(cont *container) {
	c.mapMutex.Lock()
	c.containers[cont.id] = cont
	c.mapMutex.Unlock()
}
func (c *client) deleteContainer(friendlyName string) {
	c.mapMutex.Lock()
	delete(c.containers, friendlyName)
	c.mapMutex.Unlock()
}

func (c *client) getContainer(id string) (*container, error) {
	c.mapMutex.RLock()
	container, ok := c.containers[id]
	defer c.mapMutex.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid container: %s", id) // fixme: typed error
	}
	return container, nil
}
