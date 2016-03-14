package libcontainerd

import (
	"sync"
)

func (r *remote) Client(b Backend) (Client, error) {
	c := &client{
		clientCommon: clientCommon{
			backend:          b,
			containerMutexes: make(map[string]*sync.Mutex),
			containers:       make(map[string]*container),
		},
	}

	r.setClientPlatformFields(c)

	r.Lock()
	r.clients = append(r.clients, c)
	r.Unlock()
	return c, nil
}
