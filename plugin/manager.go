package plugin

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/mount"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/registry"
	"github.com/pkg/errors"
)

const configFileName = "config.json"
const rootfsFileName = "rootfs"

func (pm *Manager) restorePlugin(p *Plugin) error {
	p.Restore(pm.config.ExecRoot)
	if p.IsEnabled() {
		return pm.restore(p)
	}
	return nil
}

type eventLogger func(id, name, action string)

type ManagerConfig struct {
	Store              *Store // remove
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
	cMap             map[*Plugin]*controller
	containerdClient libcontainerd.Client
	blobStore        *basicBlobStore
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
		return nil, errors.Wrapf(err, "failed to mkdir %v", manager.config.Root)
	}
	if err := os.MkdirAll(manager.config.ExecRoot, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to mkdir %v", manager.config.ExecRoot)
	}
	if err := os.MkdirAll(manager.tmpDir(), 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to mkdir %v", manager.tmpDir())
	}
	var err error
	manager.containerdClient, err = config.Executor.Client(manager) // todo: move to another struct
	if err != nil {
		return nil, errors.Wrap(err, "failed to create containerd client")
	}
	manager.blobStore, err = newBasicBlobStore(filepath.Join(manager.config.Root, "storage"))
	if err != nil {
		return nil, err
	}

	manager.cMap = make(map[*Plugin]*controller)
	if err := manager.reload(); err != nil {
		return nil, errors.Wrap(err, "failed to restore plugins")
	}
	return manager, nil
}

func (pm *Manager) tmpDir() string {
	return filepath.Join(pm.config.Root, "_tmp")
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

func (pm *Manager) reload() error { // todo: restore
	dir, err := ioutil.ReadDir(pm.config.Root)
	if err != nil {
		return errors.Wrapf(err, "failed to read %v", pm.config.Root)
	}
	plugins := make(map[string]*Plugin)
	for _, v := range dir {
		if validFullID.MatchString(v.Name()) {
			p, err := pm.loadPlugin(v.Name())
			if err != nil {
				return err
			}
			plugins[p.GetID()] = p
		}
	}

	pm.config.Store.SetAll(plugins)

	var wg sync.WaitGroup
	wg.Add(len(plugins))
	for _, p := range plugins {
		c := &controller{} // todo: remove this
		pm.cMap[p] = c
		go func(p *Plugin) {
			defer wg.Done()
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
	wg.Wait()
	return nil
}

func (pm *Manager) loadPlugin(id string) (*Plugin, error) {
	p := filepath.Join(pm.config.Root, id, configFileName)
	dt, err := ioutil.ReadFile(p)
	if err != nil {
		return nil, errors.Wrapf(err, "error reading %v", p)
	}
	var plugin Plugin
	if err := json.Unmarshal(dt, &plugin); err != nil {
		return nil, errors.Wrapf(err, "error decoding %v", p)
	}
	return &plugin, nil
}

// createPlugin creates a new plugin. take lock before calling.
func (pm *Manager) createPlugin(name string, configDigest digest.Digest, blobsums []digest.Digest, rootfsDir string) (err error) {
	if v, _ := pm.config.Store.GetByName(name); v != nil { // todo: this check is wrong. remove store
		return errors.Errorf("plugin %q already exists", name)
	}

	configRC, err := pm.blobStore.Get(configDigest)
	if err != nil {
		return err
	}
	defer configRC.Close()

	var config types.PluginConfig
	dec := json.NewDecoder(configRC)
	if err := dec.Decode(&config); err != nil {
		return errors.Wrapf(err, "failed to parse config")
	}
	if dec.More() {
		return errors.New("invalid config json")
	}

	p := &Plugin{
		PluginObj: types.Plugin{
			Name:   name,
			ID:     stringid.GenerateRandomID(),
			Config: config,
		},
		Config:   configDigest,
		Blobsums: blobsums,
	}
	p.initEmptySettings()

	pdir := filepath.Join(pm.config.Root, p.PluginObj.ID)
	if err := os.MkdirAll(pdir, 0700); err != nil {
		return errors.Wrapf(err, "failed to mkdir %v", pdir)
	}

	defer func() {
		if err != nil {
			os.RemoveAll(pdir)
		}
	}()

	if err := os.Rename(rootfsDir, filepath.Join(pdir, rootfsFileName)); err != nil {
		return errors.Wrap(err, "failed to rename rootfs")
	}

	// plugin.save()
	pluginJSON, err := json.Marshal(p)
	if err := ioutils.AtomicWriteFile(filepath.Join(pdir, configFileName), pluginJSON, 0600); err != nil {
		return err
	}

	pm.config.Store.Add(p) // todo: remove

	return nil
}

func (pm *Manager) cleanupUnusedBlobs(d ...digest.Digest) {
	// todo:
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
