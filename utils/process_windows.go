package utils

import (
	"os"
)

func IsProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err == nil {
		return true
	}

	return false
}

func KillProcess(pid int) {
	p, err := os.FindProcess(pid)
	if err == nil {
		p.Kill()
	}
}
