package libcontainerd

import (
	"io"
	"syscall"

	"strings"

	"github.com/Microsoft/hcsshim"
	"github.com/Sirupsen/logrus"
)

type container struct {
	containerCommon

	// Platform specific fields are below here. There are none presently on Windows.
}

func (ctr *container) start() error {
	var err error

	// Start the container
	logrus.Debugln("Starting container ", ctr.containerID)
	if err = hcsshim.StartComputeSystem(ctr.containerID); err != nil {
		logrus.Errorf("Failed to start compute system: %s", err)
		return err
	}

	createProcessParms := hcsshim.CreateProcessParams{
		EmulateConsole:   ctr.process.ociProcess.Terminal,
		WorkingDirectory: ctr.process.ociProcess.Cwd,
		ConsoleSize:      ctr.process.ociProcess.InitialConsoleSize,
	}

	// Configure the environment for the process
	createProcessParms.Environment = setupEnvironmentVariables(ctr.process.ociProcess.Env)

	// Convert the args array into the escaped command line.
	for i, arg := range ctr.process.ociProcess.Args {
		// Likely bugbug - we probably don't want to update the args, but just build locally. @darrenstahlmsft?
		ctr.process.ociProcess.Args[i] = syscall.EscapeArg(arg)
	}
	createProcessParms.CommandLine = strings.Join(ctr.process.ociProcess.Args, " ")
	logrus.Debugf("commandLine: %s", createProcessParms.CommandLine)

	iopipe := &IOPipe{Terminal: ctr.process.ociProcess.Terminal}

	var (
		pid            uint32
		stdout, stderr io.ReadCloser
	)
	// Start the command running in the container.
	pid, iopipe.Stdin, stdout, stderr, err = hcsshim.CreateProcessInComputeSystem(
		ctr.containerID,
		true,
		true,
		!ctr.process.ociProcess.Terminal,
		createProcessParms)
	if err != nil {
		logrus.Errorf("CreateProcessInComputeSystem() failed %s", err)
		return err
	}

	// Convert io.ReadClosers to io.Readers
	if stdout != nil {
		iopipe.Stdout = openReaderFromPipe(stdout)
	}
	if stderr != nil {
		iopipe.Stderr = openReaderFromPipe(stderr)
	}

	//Save the PID as we'll need this in Kill()
	logrus.Debugf("Process started - PID %d", pid)
	ctr.systemPid = uint32(pid)

	// Spin up a go routine waiting for exit to handle cleanup
	go ctr.waitExit(pid, true)

	ctr.client.appendContainer(ctr)

	// FIXME: is there a race for closing stdin before container starts
	if err := ctr.client.backend.AttachStreams(ctr.containerID, *iopipe); err != nil {
		return err
	}

	return ctr.client.backend.StateChanged(ctr.containerID, StateInfo{
		State: StateStart,
		Pid:   ctr.systemPid,
	})

}

// waitExit runs as a goroutine waiting for the process to exit. It's
// equivalent to (in the linux containerd world) where events come in for
// state change notifications from containerd.
func (ctr *container) waitExit(pid uint32, isFirstProcessToStart bool) error {
	logrus.Debugln("waitExit on ", pid)

	// Block indefinitely for the process to exit.
	exitCode, err := hcsshim.WaitForProcessInComputeSystem(ctr.containerID, pid, hcsshim.TimeoutInfinite)
	if err != nil {
		if herr, ok := err.(*hcsshim.HcsError); ok && herr.Err != syscall.ERROR_BROKEN_PIPE {
			logrus.Warnf("WaitForProcessInComputeSystem failed (container may have been killed): %s", err)
		}
		return nil
	}

	// Assume the container has exited
	st := StateInfo{
		State:    StateExit,
		ExitCode: uint32(exitCode),
	}

	// But it could have been an exec'd process which exited
	if !isFirstProcessToStart {
		st.State = StateExitProcess
	}

	// If this is the init process, always call into vmcompute.dll to
	// shutdown the container after we have completed.
	if isFirstProcessToStart {
		logrus.Debugf("Shutting down container %s", ctr.containerID)
		if err := hcsshim.ShutdownComputeSystem(ctr.containerID, hcsshim.TimeoutInfinite, "waitExit"); err != nil {
			if herr, ok := err.(*hcsshim.HcsError); !ok ||
				(herr.Err != hcsshim.ERROR_SHUTDOWN_IN_PROGRESS &&
					herr.Err != ErrorBadPathname &&
					herr.Err != syscall.ERROR_PATH_NOT_FOUND) {
				logrus.Warnf("Ignoring error from ShutdownComputeSystem %s", err)
			}
		} else {
			logrus.Debugf("Completed shutting down container %s", ctr.containerID)
		}
	}

	// BUGBUG - Is taking the lock necessary here? Should it just be taken for
	// the deleteContainer call, not for the restart logic? @jhowardmsft
	ctr.client.lock(ctr.containerID)
	defer ctr.client.unlock(ctr.containerID)

	if st.State == StateExit && ctr.restartManager != nil {
		restart, wait, err := ctr.restartManager.ShouldRestart(uint32(exitCode))
		if err != nil {
			logrus.Error(err)
		} else if restart {
			st.State = StateRestart
			ctr.restarting = true
			go func() {
				err := <-wait
				ctr.restarting = false
				if err != nil {
					logrus.Error(err)
				} else {
					ctr.start()
				}
			}()
		}
	}

	// Remove process from list if we have exited
	// We need to do so here in case the Message Handler decides to restart it.
	ctr.client.deleteContainer(ctr.friendlyName)

	// Call into the backend to notify it of the state change.
	logrus.Debugln("waitExit() calling backend.StateChanged %v", st)
	if err := ctr.client.backend.StateChanged(ctr.containerID, st); err != nil {
		logrus.Error(err)
	}

	logrus.Debugln("waitExit() completed OK")
	return nil
}
