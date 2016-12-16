package plugin

import (
	"sync"

	"github.com/docker/docker/pkg/plugins"
)

// Store manages the plugin inventory in memory and on-disk
type Store struct {
	sync.RWMutex
	plugins map[string]*Plugin
	/* handlers are necessary for transition path of legacy plugins
	 * to the new model. Legacy plugins use Handle() for registering an
	 * activation callback.*/
	handlers map[string][]func(string, *plugins.Client)
}

// NewStore creates a Store.
func NewStore(libRoot string) *Store {
	return &Store{
		plugins:  make(map[string]*Plugin),
		handlers: make(map[string][]func(string, *plugins.Client)),
	}
}
