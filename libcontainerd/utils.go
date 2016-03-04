package libcontainerd

import (
	"sync"

	containerd "github.com/docker/containerd/api/grpc/types"
)

func systemPid(c *containerd.Container) uint32 {
	var pid uint32
	for _, p := range c.Processes {
		if p.Pid == initProcessID {
			pid = p.SystemPid
		}
	}
	return pid
}

type queue struct {
	sync.Mutex
	fns map[string]chan struct{}
}

func (q *queue) append(id string, f func()) {
	q.Lock()
	defer q.Unlock()

	if q.fns == nil {
		q.fns = make(map[string]chan struct{})
	}

	done := make(chan struct{})

	fn, ok := q.fns[id]
	q.fns[id] = done
	go func() {
		if ok {
			<-fn
		}
		f()
		close(done)
	}()
}
