package utils

import (
	"syscall"
)

func IsProcessAlive(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	if err == nil || err == syscall.EPERM {
		return true
	}

	return false
}

func KillProcess(pid int) {
	syscall.Kill(pid, syscall.SIGKILL)
}
