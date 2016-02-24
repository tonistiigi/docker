package daemon

import (
	"fmt"
	"io"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/runconfig"
)

// StateChanged updates daemon state changes from containerd
func (daemon *Daemon) StateChanged(id string, e libcontainerd.StateInfo) error {
	c := daemon.containers.Get(id)
	if c == nil {
		return fmt.Errorf("no such container: %s", id)
	}

	switch e.State {
	case libcontainerd.StateExit:
		c.Reset(true)
		daemon.Cleanup(c)
		c.SetStopped(&container.ExitStatus{
			ExitCode:  int(e.ExitCode),
			OOMKilled: e.OOMKilled,
		})
		attributes := map[string]string{
			"exitCode": fmt.Sprintf("%d", e.ExitCode),
		}
		daemon.LogContainerEventWithAttributes(c, "die", attributes)

		// FIXME: here is race condition between two RUN instructions in Dockerfile
		// because they share same runconfig and change image. Must be fixed
		// in builder/builder.go
		return c.ToDisk()
	case libcontainerd.StateRestart:
		c.Reset(true)
		c.RestartCount++
		c.SetRestarting(&container.ExitStatus{
			ExitCode:  int(e.ExitCode),
			OOMKilled: e.OOMKilled,
		})
		attributes := map[string]string{
			"exitCode": fmt.Sprintf("%d", e.ExitCode),
		}
		daemon.LogContainerEventWithAttributes(c, "die", attributes)
		return c.ToDisk()
	case libcontainerd.StateExitProcess:
		if execConfig := c.ExecCommands.Get(e.ProcessID); execConfig != nil {
			ec := int(e.ExitCode)
			execConfig.ExitCode = &ec
			execConfig.Running = false
			time.Sleep(100 * time.Millisecond) // FIXME(tonistiigi)
			if err := execConfig.CloseStreams(); err != nil {
				logrus.Errorf("%s: %s", c.ID, err)
			}

			// remove the exec command from the container's store only and not the
			// daemon's store so that the exec command can be inspected.
			c.ExecCommands.Delete(execConfig.ID)
		}
	case libcontainerd.StateStart, libcontainerd.StateRestore:
		c.SetRunning(int(e.Pid), e.State == libcontainerd.StateStart)
		if err := c.ToDisk(); err != nil {
			c.Reset(true)
			return err
		}
	case libcontainerd.StatePause:
		c.Paused = true
		daemon.LogContainerEvent(c, "pause")
	case libcontainerd.StateResume:
		c.Paused = false
		daemon.LogContainerEvent(c, "unpause")
	}

	return nil
}

// AttachStreams is called by libcontainerd to connect the stdio.
func (daemon *Daemon) AttachStreams(id string, iop libcontainerd.IOPipe) error {
	var s *runconfig.StreamConfig
	if c := daemon.containers.Get(id); c == nil {
		ec, err := daemon.getExecConfig(id)
		if err != nil {
			return fmt.Errorf("no such exec/container: %s", id)
		}
		s = ec.StreamConfig
	} else {
		s = c.StreamConfig
		if err := daemon.StartLogging(c); err != nil {
			c.Reset(false)
			return err
		}
	}

	if stdin := s.Stdin(); stdin != nil {
		go func() {
			io.Copy(iop.Stdin, stdin)
			iop.Stdin.Close()
		}()
	} else {
		iop.Stdin.Close()
	}
	go func() {
		// FIXME: remove log
		logrus.Debugf(">stdout %s", id)
		n, err := io.Copy(s.Stdout(), iop.Stdout)
		logrus.Debugf("<stdout %s %d %v", id, n, err)
	}()
	go func() {
		logrus.Debugf(">stderr %s", id)
		n, err := io.Copy(s.Stderr(), iop.Stderr)
		logrus.Debugf("<stderr %s %d %v", id, n, err)
	}()

	return nil
}
