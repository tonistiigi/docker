package plugin

import enginetypes "github.com/docker/engine-api/types"

type Backend interface {
	Load(name, version string) error
	Activate(name string) error
	Disable(name string) error
	Delete(name string) error
	List() ([]enginetypes.Plugin, error)
}
