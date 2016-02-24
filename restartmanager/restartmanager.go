package restartmanager

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/engine-api/types/container"
)

const (
	backoffMultiplier = 2
	defaultTimeout    = 100 * time.Millisecond
)

type RestartManager interface {
	Cancel() error
	ShouldRestart(exitCode uint32) (bool, chan error, error)
}

type restartManager struct {
	sync.Once
	policy       container.RestartPolicy
	failureCount int
	timeout      time.Duration
	active       int32
	cancel       chan struct{}
	canceled     bool
}

func New(policy container.RestartPolicy) RestartManager {
	return &restartManager{policy: policy, cancel: make(chan struct{})}
}

func (rm *restartManager) ShouldRestart(exitCode uint32) (bool, chan error, error) {
	if !atomic.CompareAndSwapInt32(&rm.active, 0, 1) {
		return false, nil, fmt.Errorf("invalid call on active restartmanager")
	}
	if rm.canceled {
		return false, nil, nil
	}

	if exitCode != 0 {
		rm.failureCount++
	} else {
		rm.failureCount = 0
	}

	if rm.timeout == 0 {
		rm.timeout = defaultTimeout
	} else {
		rm.timeout *= backoffMultiplier
	}

	var restart bool
	switch {
	case rm.policy.IsAlways(), rm.policy.IsUnlessStopped():
		restart = true
	case rm.policy.IsOnFailure():
		// the default value of 0 for MaximumRetryCount means that we will not enforce a maximum count
		if max := rm.policy.MaximumRetryCount; max == 0 || rm.failureCount <= max {
			restart = exitCode != 0
		}
	}

	if !restart {
		rm.active = 0
		return false, nil, nil
	}

	ch := make(chan error)
	go func() {
		select {
		case <-rm.cancel:
			ch <- fmt.Errorf("restartmanager canceled")
			close(ch)
		case <-time.After(rm.timeout):
			close(ch)
		}
		rm.active = 0
	}()

	return true, ch, nil
}

func (rm *restartManager) Cancel() error {
	rm.Do(func() {
		rm.canceled = true
		close(rm.cancel)
	})
	return nil
}
