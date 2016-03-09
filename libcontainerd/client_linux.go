package libcontainerd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/pkg/idtools"
	"github.com/opencontainers/specs"
	"golang.org/x/net/context"
)

func (c *client) AddProcess(id, processID string, specp Process) error {
	c.lock(id)
	defer c.unlock(id)
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}

	spec, err := container.spec()
	if err != nil {
		return err
	}
	sp := spec.Process
	sp.Args = specp.Args
	sp.Terminal = specp.Terminal
	if specp.Env != nil {
		sp.Env = specp.Env
	}
	if specp.Cwd != nil {
		sp.Cwd = *specp.Cwd
	}
	if specp.User != nil {
		sp.User = specs.User{
			UID:            specp.User.UID,
			GID:            specp.User.GID,
			AdditionalGids: specp.User.AdditionalGids,
		}
	}
	if specp.Capabilities != nil {
		sp.Capabilities = specp.Capabilities
	}

	p := container.newProcess(processID)

	r := &containerd.AddProcessRequest{
		Args:     sp.Args,
		Cwd:      sp.Cwd,
		Terminal: sp.Terminal,
		Id:       id,
		Env:      sp.Env,
		User: &containerd.User{
			Uid:            sp.User.UID,
			Gid:            sp.User.GID,
			AdditionalGids: sp.User.AdditionalGids,
		},
		Pid:             processID,
		Stdin:           p.fifo(syscall.Stdin),
		Stdout:          p.fifo(syscall.Stdout),
		Stderr:          p.fifo(syscall.Stderr),
		Capabilities:    sp.Capabilities,
		ApparmorProfile: sp.ApparmorProfile,
		SelinuxLabel:    sp.SelinuxLabel,
		NoNewPrivileges: sp.NoNewPrivileges,
	}

	iopipe, err := p.openFifos(sp.Terminal)
	if err != nil {
		return err
	}

	if _, err := c.remote.apiClient.AddProcess(context.Background(), r); err != nil {
		p.closeFifos(iopipe)
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

func (c *client) prepareBundleDir(uid, gid int) (string, error) {
	root, err := filepath.Abs(c.remote.stateDir)
	if err != nil {
		return "", err
	}
	if uid == 0 && gid == 0 {
		return root, nil
	}
	p := string(filepath.Separator)
	for _, d := range strings.Split(root, string(filepath.Separator))[1:] {
		p = filepath.Join(p, d)
		fi, err := os.Stat(p)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if os.IsNotExist(err) || fi.Mode()&1 == 0 {
			p = fmt.Sprintf("%s.%d.%d", p, uid, gid)
			if err := idtools.MkdirAs(p, 0700, uid, gid); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
	}
	return p, nil
}

func (c *client) Create(id string, spec Spec, options ...CreateOption) (err error) {
	c.lock(id)
	defer c.unlock(id)

	if c, err := c.getContainer(id); err == nil {
		if c.restarting { // docker doesn't actually call start if restart is on atm, but probably should in the future
			c.restartManager.Cancel()
			c.clean()
		} else {
			return fmt.Errorf("Container %s is aleady active", id)
		}
	}

	uid, gid, err := getRootIDs(specs.LinuxSpec(spec))
	if err != nil {
		return err
	}
	dir, err := c.prepareBundleDir(uid, gid)
	if err != nil {
		return err
	}

	container := c.newContainer(filepath.Join(dir, id), options...)
	if err := container.clean(); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			c.deleteContainer(id)
		}
	}()

	// uid/gid
	rootfsDir := filepath.Join(container.dir, "rootfs")
	if err := idtools.MkdirAllAs(rootfsDir, 0700, uid, gid); err != nil && !os.IsExist(err) {
		return err
	}
	if err := syscall.Mount(spec.Root.Path, rootfsDir, "bind", syscall.MS_REC|syscall.MS_BIND, ""); err != nil {
		return err
	}
	spec.Root.Path = "rootfs"

	f, err := os.Create(filepath.Join(container.dir, configFilename))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(spec); err != nil {
		return err
	}

	return container.start()
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

	var terminal bool
	for _, p := range cont.Processes {
		if p.Pid == initProcessID {
			terminal = p.Terminal
		}
	}

	iopipe, err := container.openFifos(terminal)
	if err != nil {
		return err
	}

	if err := c.backend.AttachStreams(id, *iopipe); err != nil {
		return err
	}

	c.appendContainer(container)

	err = c.backend.StateChanged(id, StateInfo{
		State: StateRestore,
		Pid:   container.systemPid,
	})

	if err != nil {
		return err
	}

	if event, ok := c.remote.pastEvents[id]; ok {
		// This should only be a pause or resume event
		if event.Type == StatePause || event.Type == StateResume {
			return c.backend.StateChanged(id, StateInfo{
				State: event.Type,
				Pid:   container.systemPid,
			})
		}

		logrus.Warnf("unexpected backlog event: %#v", event)
	}

	return nil
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

func (c *client) Stats(id string) (*Stats, error) {
	resp, err := c.remote.apiClient.Stats(context.Background(), &containerd.StatsRequest{id})
	if err != nil {
		return nil, err
	}
	return (*Stats)(resp), nil
}

func (c *client) Restore(id string, options ...CreateOption) error {
	cont, err := c.getContainerdContainer(id)
	if err == nil {
		if err := c.restore(cont, options...); err != nil {
			logrus.Errorf("error restoring %s: %v", id, err)
		}
		return nil
	}
	c.lock(id)
	defer c.unlock(id)

	var exitCode uint32
	if event, ok := c.remote.pastEvents[id]; ok {
		exitCode = event.Status
		delete(c.remote.pastEvents, id)
	}

	return c.backend.StateChanged(id, StateInfo{
		State:    StateExit,
		ExitCode: exitCode,
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
	c.mapMutex.RLock()
	container, ok := c.containers[id]
	defer c.mapMutex.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid container: %s", id) // fixme: typed error
	}
	return container, nil
}

func (c *client) UpdateResources(id string, resources Resources) error {
	c.lock(id)
	defer c.unlock(id)
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}
	if container.systemPid == 0 {
		return fmt.Errorf("No active process for container %s", id)
	}
	_, err = c.remote.apiClient.UpdateContainer(context.Background(), &containerd.UpdateContainerRequest{
		Id:        id,
		Pid:       initProcessID,
		Resources: (*containerd.UpdateResource)(&resources),
	})
	if err != nil {
		return err
	}
	return nil
}
