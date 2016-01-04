package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/oci"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/plugin/distribution"
	"github.com/docker/docker/plugin/types"
	"github.com/docker/docker/restartmanager"
	enginetypes "github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/opencontainers/specs"
)

type record struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Active         bool   `json:"active"`
	Id             string `json:"id"`
	restartManager restartmanager.RestartManager
}

type PluginManager struct {
	sync.RWMutex
	root      string
	plugins   map[string]*record
	apiClient libcontainerd.Client
}

func NewManager(root string, remote libcontainerd.Remote) (*PluginManager, error) {
	root = filepath.Join(root, "plugins")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	pm := &PluginManager{
		root:    root,
		plugins: make(map[string]*record),
	}
	if err := pm.init(); err != nil {
		return nil, err
	}
	client, err := remote.Client(pm)
	if err != nil {
		return nil, err
	}
	pm.apiClient = client
	return pm, nil
}

func (pm *PluginManager) init() error {
	dt, err := ioutil.ReadFile(filepath.Join(pm.root, "plugins"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(dt, &pm.plugins); err != nil {
		return err
	}
	// FIXME: validate, restore
	return nil
}

func (pm *PluginManager) get(name string) *record {
	pm.RLock()
	r := pm.plugins[name]
	pm.RUnlock()
	return r
}

func (pm *PluginManager) Load(name, version string) error {
	if r := pm.get(name); r != nil {
		return fmt.Errorf("%s exists, version %s", name, r.Version)
	}

	// FIXME: locks
	registry := os.Getenv("PLUGIN_REGISTRY")
	if registry == "" {
		return fmt.Errorf("PLUGIN_REGISTRY not set")
	}

	pd, err := distribution.Pull(registry, fmt.Sprintf("%s:%s", name, version))
	if err != nil {
		return err
	}

	if err := distribution.WritePullData(pd, filepath.Join(pm.root, name), true); err != nil {
		return err
	}

	r := &record{
		Name:    name,
		Version: version,
	}
	pm.Lock()
	pm.plugins[name] = r
	pm.save()
	pm.Unlock()
	return nil
}
func (pm *PluginManager) Activate(name string) error {
	r := pm.get(name)
	if r == nil {
		return fmt.Errorf("no such plugin %s", name)
	}
	id := stringid.GenerateNonCryptoID()
	dt, err := ioutil.ReadFile(filepath.Join(pm.root, name, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p types.Plugin
	if err := json.Unmarshal(dt, &p); err != nil {
		return err
	}

	bundleDir := filepath.Join(pm.root, id)
	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		return err
	}

	spec, err := pm.initSpec(&p)
	if err != nil {
		return err
	}

	rm := restartmanager.New(container.RestartPolicy{
		Name: "always",
	})

	if err := pm.apiClient.Create(id, spec, libcontainerd.WithRestartManager(rm)); err != nil { // POC-only
		return err
	}

	pm.Lock() // fixme: lock single record
	r.Id = id
	r.Active = true
	pm.save()
	pm.Unlock()
	return nil
}

func (pm *PluginManager) initSpec(p *types.Plugin) (specs.LinuxSpec, error) {
	s := oci.DefaultSpec()
	s.Spec.Root = specs.Root{
		Path:     filepath.Join(pm.root, p.Name, "rootfs"),
		Readonly: false, // TODO: all plugins should be readonly?
	}
	cwd := p.Cwd
	if len(cwd) == 0 {
		cwd = "/"
	}
	s.Spec.Process = specs.Process{
		Args: p.Entrypoint,
		Cwd:  cwd,
		Env:  p.Env,
	}
	return s, nil
}

func (pm *PluginManager) Disable(name string) error {
	r := pm.get(name)
	if r == nil {
		return fmt.Errorf("no such plugin %s", name)
	}
	if err := pm.apiClient.Signal(r.Id, int(syscall.SIGKILL)); err != nil {
		logrus.Error(err)
	}
	pm.Lock() // fixme: lock single record
	r.Id = ""
	r.Active = false
	pm.save()
	pm.Unlock()
	return nil
}
func (pm *PluginManager) Delete(name string) error {
	r := pm.get(name)
	if r == nil {
		return fmt.Errorf("no such plugin %s", name)
	}
	if r.Active {
		return fmt.Errorf("plugin %s is active", name)
	}
	pm.Lock() // fixme: lock single record
	os.RemoveAll(filepath.Join(pm.root, name))
	delete(pm.plugins, name)
	pm.save()
	pm.Unlock()
	return nil
}
func (pm *PluginManager) List() ([]enginetypes.Plugin, error) {
	var out []enginetypes.Plugin
	for _, r := range pm.plugins {
		out = append(out, enginetypes.Plugin{
			Name:    r.Name,
			Version: r.Version,
			Active:  r.Active,
		}) //POC
	}
	return out, nil
}

// fixme: not safe
func (pm *PluginManager) save() error {
	dt, err := json.Marshal(pm.plugins)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(pm.root, "plugins"), dt, 0600)
}

// StateChanged updates daemon inter...
func (pm *PluginManager) StateChanged(id string, e libcontainerd.StateInfo) error {
	logrus.Debugf("plugin statechanged %s %#v", id, e)

	return nil
}

func (pm *PluginManager) AttachStreams(id string, iop libcontainerd.IOPipe) error {
	iop.Stdin.Close()
	go func() {
		io.Copy(os.Stderr, iop.Stdout) //POC
	}()
	go func() {
		io.Copy(os.Stderr, iop.Stderr)
	}()
	return nil
}
