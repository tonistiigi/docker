package libcontainerd

import "github.com/docker/docker/restartmanager"

type container struct {
	process
	restartManager restartmanager.RestartManager
	restarting     bool
	processes      map[string]*process
}
