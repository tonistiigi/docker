package libcontainerd

import (
	"sync"

	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/opencontainers/specs"
)

func getRootIDs(s specs.LinuxSpec) (int, int, error) {
	var hasUserns bool
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.UserNamespace {
			hasUserns = true
			break
		}
	}
	if !hasUserns {
		return 0, 0, nil
	}
	uid := hostIDFromMap(0, s.Linux.UIDMappings)
	gid := hostIDFromMap(0, s.Linux.GIDMappings)
	return uid, gid, nil
}

func hostIDFromMap(id uint32, mp []specs.IDMapping) int {
	for _, m := range mp {
		if id >= m.ContainerID && id <= m.ContainerID+m.Size-1 {
			return int(m.HostID + id - m.ContainerID)
		}
	}
	return 0
}

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
