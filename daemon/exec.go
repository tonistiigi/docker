package daemon

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/exec"
	"github.com/docker/docker/errors"
	"github.com/docker/docker/pkg/pools"
	"github.com/docker/docker/pkg/term"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/strslice"
	"github.com/opencontainers/specs"
)

func (d *Daemon) registerExecCommand(container *container.Container, config *exec.Config) {
	// Storing execs in container in order to kill them gracefully whenever the container is stopped or removed.
	container.ExecCommands.Add(config.ID, config)
	// Storing execs in daemon for easy access via remote API.
	d.execCommands.Add(config.ID, config)
}

// ExecExists looks up the exec instance and returns a bool if it exists or not.
// It will also return the error produced by `getConfig`
func (d *Daemon) ExecExists(name string) (bool, error) {
	if _, err := d.getExecConfig(name); err != nil {
		return false, err
	}
	return true, nil
}

// getExecConfig looks up the exec instance by name. If the container associated
// with the exec instance is stopped or paused, it will return an error.
func (d *Daemon) getExecConfig(name string) (*exec.Config, error) {
	ec := d.execCommands.Get(name)

	// If the exec is found but its container is not in the daemon's list of
	// containers then it must have been deleted, in which case instead of
	// saying the container isn't running, we should return a 404 so that
	// the user sees the same error now that they will after the
	// 5 minute clean-up loop is run which erases old/dead execs.

	if ec != nil {
		if container := d.containers.Get(ec.ContainerID); container != nil {
			if !container.IsRunning() {
				return nil, fmt.Errorf("Container %s is not running: %s", container.ID, container.State.String())
			}
			if container.IsPaused() {
				return nil, errExecPaused(container.ID)
			}
			if container.IsRestarting() {
				return nil, errContainerIsRestarting(container.ID)
			}
			return ec, nil
		}
	}

	return nil, errExecNotFound(name)
}

func (d *Daemon) unregisterExecCommand(container *container.Container, execConfig *exec.Config) {
	container.ExecCommands.Delete(execConfig.ID)
	d.execCommands.Delete(execConfig.ID)
}

func (d *Daemon) getActiveContainer(name string) (*container.Container, error) {
	container, err := d.GetContainer(name)
	if err != nil {
		return nil, err
	}

	if !container.IsRunning() {
		return nil, errNotRunning{container.ID}
	}
	if container.IsPaused() {
		return nil, errExecPaused(name)
	}
	if container.IsRestarting() {
		return nil, errContainerIsRestarting(container.ID)
	}
	return container, nil
}

// ContainerExecCreate sets up an exec in a running container.
func (d *Daemon) ContainerExecCreate(config *types.ExecConfig) (string, error) {
	container, err := d.getActiveContainer(config.Container)
	if err != nil {
		return "", err
	}

	cmd := strslice.StrSlice(config.Cmd)
	entrypoint, args := d.getEntrypointAndArgs(strslice.StrSlice{}, cmd)

	keys := []byte{}
	if config.DetachKeys != "" {
		keys, err = term.ToBytes(config.DetachKeys)
		if err != nil {
			logrus.Warnf("Wrong escape keys provided (%s, error: %s) using default : ctrl-p ctrl-q", config.DetachKeys, err.Error())
		}
	}

	execConfig := exec.NewConfig()
	execConfig.OpenStdin = config.AttachStdin
	execConfig.OpenStdout = config.AttachStdout
	execConfig.OpenStderr = config.AttachStderr
	execConfig.ContainerID = container.ID
	execConfig.DetachKeys = keys
	execConfig.Entrypoint = entrypoint
	execConfig.Args = args
	execConfig.Tty = config.Tty
	execConfig.Privileged = config.Privileged
	execConfig.User = config.User
	if len(execConfig.User) == 0 {
		execConfig.User = container.Config.User
	}

	d.registerExecCommand(container, execConfig)

	d.LogContainerEvent(container, "exec_create: "+execConfig.Entrypoint+" "+strings.Join(execConfig.Args, " "))

	return execConfig.ID, nil
}

// ContainerExecStart starts a previously set up exec instance. The
// std streams are set up.
func (d *Daemon) ContainerExecStart(name string, stdin io.ReadCloser, stdout io.Writer, stderr io.Writer) error {
	var (
		cStdin           io.ReadCloser
		cStdout, cStderr io.Writer
	)

	ec, err := d.getExecConfig(name)
	if err != nil {
		return errExecNotFound(name)
	}

	ec.Lock()
	if ec.ExitCode != nil {
		ec.Unlock()
		err := fmt.Errorf("Error: Exec command %s has already run", ec.ID)
		return errors.NewRequestConflictError(err)
	}

	if ec.Running {
		ec.Unlock()
		return fmt.Errorf("Error: Exec command %s is already running", ec.ID)
	}
	ec.Running = true
	ec.Unlock()

	c := d.containers.Get(ec.ContainerID)
	logrus.Debugf("starting exec command %s in container %s", ec.ID, c.ID)
	d.LogContainerEvent(c, "exec_start: "+ec.Entrypoint+" "+strings.Join(ec.Args, " "))

	if ec.OpenStdin && stdin != nil {
		r, w := io.Pipe()
		go func() {
			defer w.Close()
			defer logrus.Debugf("Closing buffered stdin pipe")
			pools.Copy(w, stdin)
		}()
		cStdin = r
	}
	if ec.OpenStdout {
		cStdout = stdout
	}
	if ec.OpenStderr {
		cStderr = stderr
	}

	if ec.OpenStdin {
		ec.NewInputPipes()
	} else {
		ec.NewNopInputPipe()
	}

	r := specs.Process{
		Args:     append([]string{ec.Entrypoint}, ec.Args...),
		Terminal: ec.Tty,
	}

	attachErr := container.AttachStreams(ec.StreamConfig, ec.OpenStdin, true, ec.Tty, cStdin, cStdout, cStderr, ec.DetachKeys)

	if err := d.containerd.AddProcess(c.ID, name, r); err != nil {
		return err
	}

	err = <-attachErr
	if err != nil {
		return fmt.Errorf("attach failed with error: %v", err)
	}
	return nil
}

// execCommandGC runs a ticker to clean up the daemon references
// of exec configs that are no longer part of the container.
func (d *Daemon) execCommandGC() {
	for range time.Tick(5 * time.Minute) {
		var (
			cleaned          int
			liveExecCommands = d.containerExecIds()
		)
		for id, config := range d.execCommands.Commands() {
			if config.CanRemove {
				cleaned++
				d.execCommands.Delete(id)
			} else {
				if _, exists := liveExecCommands[id]; !exists {
					config.CanRemove = true
				}
			}
		}
		if cleaned > 0 {
			logrus.Debugf("clean %d unused exec commands", cleaned)
		}
	}
}

// containerExecIds returns a list of all the current exec ids that are in use
// and running inside a container.
func (d *Daemon) containerExecIds() map[string]struct{} {
	ids := map[string]struct{}{}
	for _, c := range d.containers.List() {
		for _, id := range c.ExecCommands.List() {
			ids[id] = struct{}{}
		}
	}
	return ids
}
