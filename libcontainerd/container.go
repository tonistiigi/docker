package libcontainerd

import (
	"fmt"

	"github.com/docker/docker/restartmanager"
)

const (
	initFriendlyName = "init"
	configFilename   = "config.json"
)

type containerCommon struct {
	process
	restartManager restartmanager.RestartManager
	restarting     bool
	processes      map[string]*process
}

// WithRestartManager sets the restartmanager to be used with the container.
func WithRestartManager(rm restartmanager.RestartManager) CreateOption {
	return restartManager{rm}
}

type restartManager struct {
	rm restartmanager.RestartManager
}

func (rm restartManager) Apply(p interface{}) error {
	if pr, ok := p.(*container); ok {
		pr.restartManager = rm.rm
		return nil
	}
	return fmt.Errorf("WithRestartManager option not supported for this client")
}
