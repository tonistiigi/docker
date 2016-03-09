package libcontainerd

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/restartmanager"
	"github.com/opencontainers/specs"
	"golang.org/x/net/context"
)

const (
	configFilename = "config.json"
	initProcessID  = "init"
)

type container struct {
	process
	pauseMonitor
	restartManager restartmanager.RestartManager
	restarting     bool
	oom            bool
	processes      map[string]*process
}

func (c *container) clean() error {
	if _, err := os.Lstat(c.dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	syscall.Unmount(filepath.Join(c.dir, "rootfs"), syscall.MNT_DETACH) // ignore error
	if err := os.RemoveAll(c.dir); err != nil {
		return err
	}
	return nil
}

func (c *container) spec() (*specs.Spec, error) {
	var spec specs.Spec
	dt, err := ioutil.ReadFile(filepath.Join(c.dir, configFilename))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dt, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (c *container) start() error {
	spec, err := c.spec()
	if err != nil {
		return nil
	}
	iopipe, err := c.openFifos(spec.Process.Terminal)
	if err != nil {
		return err
	}

	r := &containerd.CreateContainerRequest{
		Id:         c.id,
		BundlePath: c.dir,
		Stdin:      c.fifo(syscall.Stdin),
		Stdout:     c.fifo(syscall.Stdout),
		Stderr:     c.fifo(syscall.Stderr),
	}
	c.client.appendContainer(c)

	resp, err := c.client.remote.apiClient.CreateContainer(context.Background(), r)
	if err != nil {
		return err
	}

	// FIXME: is there a race for closing stdin before container starts
	if err := c.client.backend.AttachStreams(c.id, *iopipe); err != nil {
		return err
	}
	c.systemPid = systemPid(resp.Container)

	return c.client.backend.StateChanged(c.id, StateInfo{
		State: StateStart,
		Pid:   c.systemPid,
	})
}

func (c *container) newProcess(id string) *process {
	return &process{
		id:        c.id,
		processID: id,
		dir:       c.dir,
		client:    c.client,
	}
}

func (c *container) handleEvent(e *containerd.Event) error {
	c.client.lock(c.id)
	defer c.client.unlock(c.id)
	switch e.Type {
	case StateExit, StatePause, StateResume, StateOOM:
		st := StateInfo{
			State:     e.Type,
			ExitCode:  e.Status,
			OOMKilled: e.Type == StateExit && c.oom,
		}
		if e.Type == StateOOM {
			c.oom = true
		}
		if e.Type == StateExit && e.Pid != initProcessID {
			st.ProcessID = e.Pid
			st.State = StateExitProcess
		}
		if st.State == StateExit && c.restartManager != nil {
			restart, wait, err := c.restartManager.ShouldRestart(e.Status)
			if err != nil {
				logrus.Error(err)
			} else if restart {
				st.State = StateRestart
				c.restarting = true
				go func() {
					err := <-wait
					c.restarting = false
					if err != nil {
						logrus.Error(err)
					} else {
						c.start()
					}
				}()
			}
		}

		// Remove process from list if we have exited
		// We need to do so here in case the Message Handler decides to restart it.
		if st.State == StateExit {
			if os.Getenv("LIBCONTAINERD_NOCLEAN") != "1" {
				c.clean()
			}
			c.client.deleteContainer(e.Id)
		}
		c.client.q.append(e.Id, func() {
			if err := c.client.backend.StateChanged(e.Id, st); err != nil {
				logrus.Error(err)
			}
			if e.Type == StatePause || e.Type == StateResume {
				c.pauseMonitor.handle(e.Type)
			}
		})

	default:
		logrus.Debugf("event unhandled: %+v", e)
	}
	return nil
}
