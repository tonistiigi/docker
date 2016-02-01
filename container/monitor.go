package container

import (
	"time"

	"github.com/Sirupsen/logrus"
)

const (
	defaultTimeIncrement = 100
	loggerCloseTimeout   = 10 * time.Second
)

// supervisor defines the interface that a supervisor must implement
type supervisor interface {
	// LogContainerEvent generates events related to a given container
	LogContainerEvent(*Container, string)
	// Cleanup ensures that the container is properly unmounted
	Cleanup(*Container)
	// StartLogging starts the logging driver for the container
	StartLogging(*Container) error
	// Run starts a container
	Run(c *Container) error
	// IsShuttingDown tells whether the supervisor is shutting down or not
	IsShuttingDown() bool
}

func (container *Container) Reset(lock bool) {
	logrus.Debugf("resetting %d\n", container.ID)

	if lock {
		container.Lock()
		defer container.Unlock()
	}

	if err := container.CloseStreams(); err != nil {
		logrus.Errorf("%s: %s", container.ID, err)
	}

	// if container.Command != nil && container.Command.ProcessConfig.Terminal != nil {
	// 	if err := container.Command.ProcessConfig.Terminal.Close(); err != nil {
	// 		logrus.Errorf("%s: Error closing terminal: %s", container.ID, err)
	// 	}
	// }

	// Re-create a brand new stdin pipe once the container exited
	if container.Config.OpenStdin {
		container.NewInputPipes()
	}

	if container.LogDriver != nil {
		if container.LogCopier != nil {
			exit := make(chan struct{})
			go func() {
				container.LogCopier.Wait()
				close(exit)
			}()
			select {
			case <-time.After(loggerCloseTimeout):
				logrus.Warnf("Logger didn't exit in time: logs may be truncated")
			case <-exit:
			}
		}
		container.LogDriver.Close()
		container.LogCopier = nil
		container.LogDriver = nil
	}
}

// Fixme: move to daemon? expose reset
func (container *Container) Start(s supervisor) error {
	// if err := s.StartLogging(container); err != nil {
	// 	container.Reset(false)
	// 	return err
	// }

	container.RestartCount = 0

	err := s.Run(container)
	if err != nil {
		container.Reset(false)
		return err
	}

	if container.Config.Tty {
		// The callback is called after the process start()
		// so we are in the parent process. In TTY mode, stdin/out/err is the PtySlave
		// which we close here. FIXME
		// if c, ok := container.Stdout().(io.Closer); ok {
		// 		c.Close()
		// 	}
	}

	return nil
}
