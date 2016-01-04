package daemon

import (
	"fmt"
	"log"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/libcontainerd"
)

func (daemon *Daemon) StateChanged(id string, e libcontainerd.StateInfo) error {
	container := daemon.containers.Get(id)
	if container == nil {
		return fmt.Errorf("no such container: %s", id)
	}

	switch e.State {
	case "exit":
		container.Reset(true)

		container.SetStopped(&execdriver.ExitStatus{ExitCode: int(e.ExitCode)}) // FIXME: OOMKilled

		// FIXME: only call this when no auto-restart
		// Cleanup networking and mounts
		daemon.Cleanup(container)

		// FIXME: here is race condition between two RUN instructions in Dockerfile
		// because they share same runconfig and change image. Must be fixed
		// in builder/builder.go
		if err := container.ToDisk(); err != nil {
			logrus.Errorf("Error dumping container %s state to disk: %s", container.ID, err)

			return err
		}
	case "started":
		container.SetRunning(int(e.Pid))

		if err := container.ToDisk(); err != nil {
			container.Reset(false)
			return err
		}
	case "paused":
		log.Println("settopause")
		container.Paused = true
		daemon.LogContainerEvent(container, "pause")
	case "running":
		log.Println("settorun")
		container.Paused = false
		daemon.LogContainerEvent(container, "unpause")
	}

	return nil
}

func (daemon *Daemon) ProcessExited(id, processId string, exitCode uint32) error {
	logrus.Debugf("process exited: id: %d process: %d exitCode: %d", id, processId, exitCode)
	container := daemon.containers.Get(id)
	if container == nil {
		return fmt.Errorf("no such container: %s", id)
	}
	execConfig := container.ExecCommands.Get(processId)
	if execConfig != nil {
		execConfig.ExitCode = int(exitCode)
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
