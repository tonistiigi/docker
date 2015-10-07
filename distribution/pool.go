package distribution

import (
	"fmt"
	"sync"

	"github.com/docker/docker/pkg/broadcaster"
)

// A Pool manages concurrent pulls and pushes. It deduplicates in-progress
// downloads and uploads, and prevents pulls from interfering with pushes
// (and vice versa).
type Pool struct {
	sync.Mutex
	pullingPool map[string]*broadcaster.Buffered
	pushingPool map[string]*broadcaster.Buffered
}

// NewPool creates a new Pool.
func NewPool() *Pool {
	return &Pool{
		pullingPool: make(map[string]*broadcaster.Buffered),
		pushingPool: make(map[string]*broadcaster.Buffered),
	}
}

// add checks if a push or pull is already running, and returns
// (broadcaster, true) if a running operation is found. Otherwise, it creates a
// new one and returns (broadcaster, false).
func (pool *Pool) add(kind, key string) (*broadcaster.Buffered, bool) {
	pool.Lock()
	defer pool.Unlock()

	if p, exists := pool.pullingPool[key]; exists {
		return p, true
	}
	if p, exists := pool.pushingPool[key]; exists {
		return p, true
	}

	broadcaster := broadcaster.NewBuffered()

	switch kind {
	case "pull":
		pool.pullingPool[key] = broadcaster
	case "push":
		pool.pushingPool[key] = broadcaster
	default:
		panic("Unknown pool type")
	}

	return broadcaster, false
}

func (pool *Pool) removeWithError(kind, key string, broadcasterResult error) error {
	pool.Lock()
	defer pool.Unlock()
	switch kind {
	case "pull":
		if broadcaster, exists := pool.pullingPool[key]; exists {
			broadcaster.CloseWithError(broadcasterResult)
			delete(pool.pullingPool, key)
		}
	case "push":
		if broadcaster, exists := pool.pushingPool[key]; exists {
			broadcaster.CloseWithError(broadcasterResult)
			delete(pool.pushingPool, key)
		}
	default:
		return fmt.Errorf("Unknown pool type")
	}
	return nil
}

func (pool *Pool) remove(kind, key string) error {
	return pool.removeWithError(kind, key, nil)
}
