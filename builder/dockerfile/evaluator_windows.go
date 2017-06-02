package dockerfile

import (
	"errors"

	"github.com/docker/docker/builder/dockerfile/instructions"
)

// platformSupports is gives users a quality error message if a Dockerfile uses
// a command not supported on the platform.
func platformSupports(command interface{}) error {
	switch command.(type) {
	case *instructions.StopSignalCommand:
		return errors.New("The daemon on this platform does not support the command stopsignal")
	}
	return nil
}
