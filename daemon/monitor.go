package daemon

import (
	"fmt"
	"log"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/libcontainerd"
)

func (daemon *Daemon) StateChanged(id string, e libcontainerd.StateInfo) error {
	c := daemon.containers.Get(id)
	if c == nil {
		return fmt.Errorf("no such container: %s", id)
	}

	switch e.State {
	case "exit":
		c.Reset(true)

		c.SetStopped(&container.ExitStatus{ExitCode: int(e.ExitCode)}) // FIXME: OOMKilled

		// FIXME: only call this when no auto-restart
		// Cleanup networking and mounts
		daemon.Cleanup(c)

		// FIXME: here is race condition between two RUN instructions in Dockerfile
		// because they share same runconfig and change image. Must be fixed
		// in builder/builder.go
		if err := c.ToDisk(); err != nil {
			logrus.Errorf("Error dumping container %s state to disk: %s", c.ID, err)

			return err
		}
	case "started":
		c.SetRunning(int(e.Pid), true)

		if err := c.ToDisk(); err != nil {
			c.Reset(false)
			return err
		}
	case "restored":
		c.SetRunning(int(e.Pid), false)

		if err := c.ToDisk(); err != nil {
			c.Reset(false)
			return err
		}
	case "paused":
		log.Println("settopause")
		c.Paused = true
		daemon.LogContainerEvent(c, "pause")
	case "running":
		log.Println("settorun")
		c.Paused = false
		daemon.LogContainerEvent(c, "unpause")
	}

	return nil
}

func (daemon *Daemon) PrepareContainerIOStreams(id string) (*libcontainerd.IO, error) {
	c := daemon.containers.Get(id)
	if c == nil {
		return nil, fmt.Errorf("no such container: %s", id)
	}
	if err := daemon.StartLogging(c); err != nil {
		c.Reset(false)
		return nil, err
	}

	return &libcontainerd.IO{
		Terminal: c.Config.Tty,
		Stdin:    c.Stdin(),
		Stdout:   c.Stdout(),
		Stderr:   c.Stderr(),
	}, nil
}

func (daemon *Daemon) ProcessExited(id, processId string, exitCode uint32) error {
	logrus.Debugf("process exited: id: %d process: %d exitCode: %d", id, processId, exitCode)
	container := daemon.containers.Get(id)
	if container == nil {
		return fmt.Errorf("no such container: %s", id)
	}
	execConfig := container.ExecCommands.Get(processId)
	if execConfig != nil {
		ec := int(exitCode)
		execConfig.ExitCode = &ec
		execConfig.Running = false

		if err := execConfig.CloseStreams(); err != nil {
			logrus.Errorf("%s: %s", container.ID, err)
		}

		// remove the exec command from the container's store only and not the
		// daemon's store so that the exec command can be inspected.
		container.ExecCommands.Delete(execConfig.ID)
	}
	return nil
}
