package plugin

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/pkg/mount"
	"github.com/docker/docker/plugin/store"
	"github.com/docker/docker/plugin/v2"
	"github.com/docker/docker/registry"
	"github.com/pkg/errors"
)

const configFileName = "config.json"

func (pm *Manager) restorePlugin(p *v2.Plugin) error {
	p.Restore(pm.config.ExecRoot)
	if p.IsEnabled() {
		return pm.restore(p)
	}
	return nil
}

type eventLogger func(id, name, action string)

type ManagerConfig struct {
	Store              *store.Store // remove
	Executor           libcontainerd.Remote
	RegistryService    registry.Service
	LiveRestoreEnabled bool // TODO: remove
	LogPluginEvent     eventLogger
	Root               string
	ExecRoot           string
}

// Manager controls the plugin subsystem.
type Manager struct {
	config           ManagerConfig
	mu               sync.RWMutex // protects cMap
	cMap             map[*v2.Plugin]*controller
	containerdClient libcontainerd.Client
}

// controller represents the manager's control on a plugin.
type controller struct {
	restart       bool
	exitChan      chan bool
	timeoutInSecs int
}

// NewManager returns a new plugin manager.
func NewManager(config ManagerConfig) (*Manager, error) {
	manager := &Manager{
		config: config,
	}
	if err := os.MkdirAll(manager.config.Root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %v", manager.config.Root)
	}
	if err := os.MkdirAll(manager.config.ExecRoot, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %v", manager.config.ExecRoot)
	}
	var err error
	manager.containerdClient, err = config.Executor.Client(manager) // todo: move to another struct
	if err != nil {
		return nil, errors.Wrap(err, "failed to create containerd client")
	}
	manager.cMap = make(map[*v2.Plugin]*controller)
	if err := manager.reload(); err != nil {
		return nil, errors.Wrap(err, "failed to restore plugins")
	}
	return manager, nil
}

// StateChanged updates plugin internals using libcontainerd events.
func (pm *Manager) StateChanged(id string, e libcontainerd.StateInfo) error {
	logrus.Debugf("plugin state changed %s %#v", id, e)

	switch e.State {
	case libcontainerd.StateExit:
		p, err := pm.config.Store.GetByID(id)
		if err != nil {
			return err
		}

		pm.mu.RLock()
		c := pm.cMap[p]

		if c.exitChan != nil {
			close(c.exitChan)
		}
		restart := c.restart
		pm.mu.RUnlock()

		p.RemoveFromDisk()

		if p.PropagatedMount != "" {
			if err := mount.Unmount(p.PropagatedMount); err != nil {
				logrus.Warnf("Could not unmount %s: %v", p.PropagatedMount, err)
			}
		}

		if restart {
			pm.enable(p, c, true)
		}
	}

	return nil
}

// reload is used on daemon restarts to load the manager's state
func (pm *Manager) reload() error {
	dt, err := os.Open(filepath.Join(pm.config.Root, "plugins.json")) // todo: remove
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer dt.Close()

	plugins := make(map[string]*v2.Plugin)
	if err := json.NewDecoder(dt).Decode(&plugins); err != nil {
		return err
	}
	pm.config.Store.SetAll(plugins)

	var group sync.WaitGroup
	group.Add(len(plugins))
	for _, p := range plugins {
		c := &controller{}
		pm.cMap[p] = c
		go func(p *v2.Plugin) {
			defer group.Done()
			if err := pm.restorePlugin(p); err != nil {
				logrus.Errorf("failed to restore plugin '%s': %s", p.Name(), err)
				return
			}

			if p.Rootfs != "" {
				p.Rootfs = filepath.Join(pm.config.Root, p.PluginObj.ID, "rootfs")
			}

			// We should only enable rootfs propagation for certain plugin types that need it.
			for _, typ := range p.PluginObj.Config.Interface.Types {
				if (typ.Capability == "volumedriver" || typ.Capability == "graphdriver") && typ.Prefix == "docker" && strings.HasPrefix(typ.Version, "1.") {
					if p.PluginObj.Config.PropagatedMount != "" {
						// TODO: sanitize PropagatedMount and prevent breakout
						p.PropagatedMount = filepath.Join(p.Rootfs, p.PluginObj.Config.PropagatedMount)
						if err := os.MkdirAll(p.PropagatedMount, 0755); err != nil {
							logrus.Errorf("failed to create PropagatedMount directory at %s: %v", p.PropagatedMount, err)
							return
						}
					}
				}
			}

			pm.config.Store.Update(p)
			requiresManualRestore := !pm.config.LiveRestoreEnabled && p.IsEnabled()

			if requiresManualRestore {
				// if liveRestore is not enabled, the plugin will be stopped now so we should enable it
				if err := pm.enable(p, c, true); err != nil {
					logrus.Errorf("failed to enable plugin '%s': %s", p.Name(), err)
				}
			}
		}(p)
	}
	group.Wait()
	return nil
}

type logHook struct{ id string }

func (logHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (l logHook) Fire(entry *logrus.Entry) error {
	entry.Data = logrus.Fields{"plugin": l.id}
	return nil
}

func attachToLog(id string) func(libcontainerd.IOPipe) error {
	return func(iop libcontainerd.IOPipe) error {
		iop.Stdin.Close()

		logger := logrus.New()
		logger.Hooks.Add(logHook{id})
		// TODO: cache writer per id
		w := logger.Writer()
		go func() {
			io.Copy(w, iop.Stdout)
		}()
		go func() {
			// TODO: update logrus and use logger.WriterLevel
			io.Copy(w, iop.Stderr)
		}()
		return nil
	}
}
