package daemon

import (
	"fmt"
	"log"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/pkg/stringid"
)

const (
	defaultTimeIncrement = 100
)

// ContainerShouldRestart returns true if container should be restarted by the
// restart manager.
func (daemon *Daemon) ContainerShouldRestart(container *container.Container, exitCode uint32) bool {
	if container.ShouldStop {
		container.HasBeenManuallyStopped = !daemon.IsShuttingDown()
		return false
	}

	if exitCode != 0 {
		container.FailureCount++
	} else {
		container.FailureCount = 0
	}

	executionTime := time.Now().Sub(container.StartedAt).Seconds()

	if executionTime >= 10 {
		container.TimeIncrement = defaultTimeIncrement
	} else {
		container.TimeIncrement *= 2
	}

	switch {
	case container.HostConfig.RestartPolicy.IsAlways(), container.HostConfig.RestartPolicy.IsUnlessStopped():
		return true
	case container.HostConfig.RestartPolicy.IsOnFailure():
		// the default value of 0 for MaximumRetryCount means that we will not enforce a maximum count
		if max := container.HostConfig.RestartPolicy.MaximumRetryCount; max != 0 && container.FailureCount > max {
			logrus.Debugf("stopping restart of container %s because maximum failure of %d has been reached",
				stringid.TruncateID(container.ID), max)
			// Reset the failureCount so next time the container is started we
			// have a grace period
			container.FailureCount = 0
			container.TimeIncrement = defaultTimeIncrement
			return false
		}

		return exitCode != 0
	}

	return false
}

// StateChanged updates daemon inter...
func (daemon *Daemon) StateChanged(id string, e libcontainerd.StateInfo) error {
	c := daemon.containers.Get(id)
	if c == nil {
		return fmt.Errorf("no such container: %s", id)
	}

	switch e.State {
	case "exit":
		c.Reset(true)

		if daemon.ContainerShouldRestart(c, e.ExitCode) {
			c.SetStopped(&container.ExitStatus{ExitCode: int(e.ExitCode)}) // FIXME: OOMKilled

			logrus.Debugf("Delaying restart by %d ms!", c.TimeIncrement)
			c.RestartTimer = time.AfterFunc(time.Duration(c.TimeIncrement)*time.Millisecond,
				func() {
					c.RestartCount++
					err := daemon.Run(c)
					if err != nil {
						logrus.Error(err)
					}
				})
		} else {
			// Cleanup networking and mounts
			daemon.Cleanup(c)

			c.SetStopped(&container.ExitStatus{ExitCode: int(e.ExitCode)}) // FIXME: OOMKilled
		}

		// FIXME: here is race condition between two RUN instructions in Dockerfile
		// because they share same runconfig and change image. Must be fixed
		// in builder/builder.go
		if err := c.ToDisk(); err != nil {
			logrus.Errorf("Error dumping container %s state to disk: %s", c.ID, err)

			return err
		}

	case "started":
		c.SetRunning(int(e.Pid), true)

		if c.TimeIncrement == 0 {
			c.TimeIncrement = defaultTimeIncrement
		}

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

// PrepareContainerIOStreams returns io streams that will be connected to the
// container process.
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

// ProcessExited is a function that is called when executed process exits.
func (daemon *Daemon) ProcessExited(id, processID string, exitCode uint32) error {
	logrus.Debugf("process exited: id: %d process: %d exitCode: %d", id, processID, exitCode)
	container := daemon.containers.Get(id)
	if container == nil {
		return fmt.Errorf("no such container: %s", id)
	}
	execConfig := container.ExecCommands.Get(processID)
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
