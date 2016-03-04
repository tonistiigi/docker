package libcontainerd

import (
	"sync"

	"github.com/Sirupsen/logrus"
)

type client struct {
	sync.Mutex                              // lock for containerMutexes map access
	mapMutex         sync.RWMutex           // protects read/write oprations from containers map
	containerMutexes map[string]*sync.Mutex // lock by container ID
	backend          Backend
	remote           *remote
	containers       map[string]*container
	q                queue
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
func (c *client) deleteContainer(id string) {
	c.mapMutex.Lock()
	delete(c.containers, id)
	c.mapMutex.Unlock()
}
