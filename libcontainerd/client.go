package libcontainerd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/opencontainers/specs"
	"golang.org/x/net/context"
)

// Client privides access to containerd features.
type Client interface {
	CreateContainer
	Signal(id string, sig int) error
	AddProcess(id, processID string, process specs.Process) error
	Resize(id, processID string, width, height int) error
	Pause(id string) error
	Resume(id string) error
	Restore(id string) error
	Stats(id string) (*containerd.StatsResponse, error)
	GetPidsForContainer(id string) ([]int, error)
}

type client struct {
	sync.Mutex
	arrayLock  sync.RWMutex
	locks      map[string]*sync.Mutex
	backend    Backend
	remote     *remote
	containers map[string]*container
}

func (c *client) Signal(id string, sig int) error {
	c.lock(id)
	defer c.unlock(id)
	if _, err := c.getContainer(id); err != nil {
		return err
	}
	_, err := c.remote.apiClient.Signal(context.Background(), &containerd.SignalRequest{
		Id:     id,
		Pid:    initProcessID,
		Signal: uint32(sig),
	})
	return err
}

func (c *client) restore(cont *containerd.Container, options ...CreateOption) (err error) {
	c.lock(cont.Id)
	defer c.unlock(cont.Id)

	logrus.Debugf("restore container %s state %s", cont.Id, cont.Status)

	id := cont.Id
	if _, err := c.getContainer(id); err == nil {
		return fmt.Errorf("container %s is aleady active", id)
	}

	defer func() {
		if err != nil {
			c.deleteContainer(cont.Id)
		}
	}()

	container := c.newContainer(cont.BundlePath, options...)
	container.systemPid = systemPid(cont)

	iopipe, err := container.openFifos()
	if err != nil {
		return err
	}

	if err := c.backend.AttachStreams(id, *iopipe); err != nil {
		return err
	}

	c.appendContainer(container)

	return c.backend.StateChanged(id, StateInfo{
		State: StateRestore,
		Pid:   container.systemPid,
	})
}

func (c *client) Resize(id, processID string, width, height int) error {
	c.lock(id)
	defer c.unlock(id)
	if _, err := c.getContainer(id); err != nil {
		return err
	}
	_, err := c.remote.apiClient.UpdateProcess(context.Background(), &containerd.UpdateProcessRequest{
		Id:     id,
		Pid:    processID,
		Width:  uint32(width),
		Height: uint32(height),
	})
	return err
}

func (c *client) AddProcess(id, processID string, specp specs.Process) error {
	c.lock(id)
	defer c.unlock(id)
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}

	var spec specs.Spec
	dt, err := ioutil.ReadFile(filepath.Join(container.dir, configFilename))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(dt, &spec); err != nil {
		return err
	}

	initProcess := spec.Process
	if specp.Env == nil {
		specp.Env = initProcess.Env
	}
	specp.User.UID = initProcess.User.UID
	specp.User.GID = initProcess.User.GID
	specp.User.AdditionalGids = initProcess.User.AdditionalGids
	if specp.Cwd == "" {
		specp.Cwd = initProcess.Cwd
	}
	p := container.newProcess(processID)

	r := &containerd.AddProcessRequest{
		Args:     specp.Args,
		Cwd:      specp.Cwd,
		Terminal: specp.Terminal,
		Id:       id,
		Env:      specp.Env,
		User: &containerd.User{
			Uid:            specp.User.UID,
			Gid:            specp.User.GID,
			AdditionalGids: specp.User.AdditionalGids,
		},
		Pid:    processID,
		Stdin:  p.fifo(syscall.Stdin),
		Stdout: p.fifo(syscall.Stdout),
		Stderr: p.fifo(syscall.Stderr),
	}

	iopipe, err := p.openFifos()
	if err != nil {
		return err
	}

	if _, err := c.remote.apiClient.AddProcess(context.Background(), r); err != nil {
		return err
	}

	container.processes[processID] = p

	c.unlock(id)

	if err := c.backend.AttachStreams(processID, *iopipe); err != nil {
		return err
	}
	c.lock(id)

	return nil
}

func (c *client) Pause(id string) error {
	return c.setState(id, StatePause)
}

func (c *client) setState(id, state string) error {
	c.lock(id)
	container, err := c.getContainer(id)
	if err != nil {
		c.unlock(id)
		return err
	}
	if container.systemPid == 0 {
		c.unlock(id)
		return fmt.Errorf("No active process for container %s", id)
	}
	st := "running"
	if state == StatePause {
		st = "paused"
	}
	chstate := make(chan struct{})
	_, err = c.remote.apiClient.UpdateContainer(context.Background(), &containerd.UpdateContainerRequest{
		Id:     id,
		Pid:    initProcessID,
		Status: st,
	})
	if err != nil {
		c.unlock(id)
		return err
	}
	container.pauseMonitor.append(state, chstate)
	c.unlock(id)
	<-chstate
	return nil
}

func (c *client) Resume(id string) error {
	return c.setState(id, StateResume)
}

func (c *client) Stats(id string) (*containerd.StatsResponse, error) {
	return c.remote.apiClient.Stats(context.Background(), &containerd.StatsRequest{id})
}

func (c *client) Restore(id string) error {
	cont, err := c.getContainerdContainer(id)
	if err == nil {
		if err := c.restore(cont); err != nil {
			logrus.Errorf("error restoring %s: %v", id, err)
		}
		return nil
	}
	c.lock(id)
	defer c.unlock(id)
	return c.backend.StateChanged(id, StateInfo{
		State: StateExit, // FIXME: properties from event log
	})
}

func (c *client) GetPidsForContainer(id string) ([]int, error) {
	cont, err := c.getContainerdContainer(id)
	if err != nil {
		return nil, err
	}
	pids := make([]int, len(cont.Pids))
	for i, p := range cont.Pids {
		pids[i] = int(p)
	}
	return pids, nil
}

func (c *client) getContainerdContainer(id string) (*containerd.Container, error) {
	resp, err := c.remote.apiClient.State(context.Background(), &containerd.StateRequest{Id: id})
	if err != nil {
		return nil, err
	}
	for _, cont := range resp.Containers {
		if cont.Id == id {
			return cont, nil
		}
	}
	return nil, fmt.Errorf("invalid state response")
}

func (c *client) newContainer(dir string, options ...CreateOption) *container {
	container := &container{
		process: process{
			id:        filepath.Base(dir),
			dir:       dir,
			client:    c,
			processID: initProcessID,
		},
		processes: make(map[string]*process),
	}
	for _, option := range options {
		if err := option.Apply(container); err != nil {
			logrus.Error(err)
		}
	}
	return container
}

func (c *client) getContainer(id string) (*container, error) {
	c.arrayLock.RLock()
	container, ok := c.containers[id]
	defer c.arrayLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid container: %s", id) // fixme: typed error
	}
	return container, nil
}

func (c *client) lock(id string) {
	c.Lock()
	if _, ok := c.locks[id]; !ok {
		c.locks[id] = &sync.Mutex{}
	}
	c.Unlock()
	c.locks[id].Lock()
}

func (c *client) unlock(id string) {
	c.Lock()
	if l, ok := c.locks[id]; ok {
		l.Unlock()
	}
	c.Unlock()
}

// must hold a lock for c.ID
func (c *client) appendContainer(cont *container) {
	c.arrayLock.Lock()
	c.containers[cont.id] = cont
	c.arrayLock.Unlock()
}
func (c *client) deleteContainer(id string) {
	c.arrayLock.Lock()
	delete(c.containers, id)
	c.arrayLock.Unlock()
}
