// +build linux

package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/pools"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/plugin/distribution"
	"github.com/docker/docker/reference"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

var (
	validFullID    = regexp.MustCompile(`^([a-f0-9]{64})$`)
	validPartialID = regexp.MustCompile(`^([a-f0-9]{1,64})$`)
)

// Disable deactivates a plugin. This means resources (volumes, networks) cant use them.
func (pm *Manager) Disable(name string, config *types.PluginDisableConfig) error {
	p, err := pm.config.Store.GetByName(name)
	if err != nil {
		return err
	}
	pm.mu.RLock()
	c := pm.cMap[p]
	pm.mu.RUnlock()

	if !config.ForceDisable && p.GetRefCount() > 0 {
		return fmt.Errorf("plugin %s is in use", p.Name())
	}

	if err := pm.disable(p, c); err != nil {
		return err
	}
	pm.config.LogPluginEvent(p.GetID(), name, "disable")
	return nil
}

// Enable activates a plugin, which implies that they are ready to be used by containers.
func (pm *Manager) Enable(name string, config *types.PluginEnableConfig) error {
	p, err := pm.config.Store.GetByName(name)
	if err != nil {
		return err
	}

	c := &controller{timeoutInSecs: config.Timeout}
	if err := pm.enable(p, c, false); err != nil {
		return err
	}
	pm.config.LogPluginEvent(p.GetID(), name, "enable")
	return nil
}

// Inspect examines a plugin config
func (pm *Manager) Inspect(refOrID string) (tp types.Plugin, err error) {
	// Match on full ID
	if validFullID.MatchString(refOrID) {
		p, err := pm.config.Store.GetByID(refOrID)
		if err == nil {
			return p.PluginObj, nil
		}
	}

	// Match on full name
	if pluginName, err := getPluginName(refOrID); err == nil {
		if p, err := pm.config.Store.GetByName(pluginName); err == nil {
			return p.PluginObj, nil
		}
	}

	// Match on partial ID
	if validPartialID.MatchString(refOrID) {
		p, err := pm.config.Store.Search(refOrID)
		if err == nil {
			return p.PluginObj, nil
		}
		return tp, err
	}

	return tp, fmt.Errorf("no such plugin name or ID associated with %q", refOrID)
}

func (pm *Manager) pull(name string, metaHeader http.Header, authConfig *types.AuthConfig) (reference.Named, distribution.PullData, error) {
	ref, err := distribution.GetRef(name)
	if err != nil {
		logrus.Debugf("error in distribution.GetRef: %v", err)
		return nil, nil, err
	}
	name = ref.String()

	if p, _ := pm.config.Store.GetByName(name); p != nil {
		logrus.Debug("plugin already exists")
		return nil, nil, fmt.Errorf("%s exists", name)
	}

	pd, err := distribution.Pull(ref, pm.config.RegistryService, metaHeader, authConfig)
	if err != nil {
		logrus.Debugf("error in distribution.Pull(): %v", err)
		return nil, nil, err
	}
	return ref, pd, nil
}

func computePrivileges(pd distribution.PullData) (types.PluginPrivileges, error) {
	config, err := pd.Config()
	if err != nil {
		return nil, err
	}

	var c types.PluginConfig
	if err := json.Unmarshal(config, &c); err != nil {
		return nil, err
	}

	var privileges types.PluginPrivileges
	if c.Network.Type != "null" && c.Network.Type != "bridge" && c.Network.Type != "" {
		privileges = append(privileges, types.PluginPrivilege{
			Name:        "network",
			Description: "permissions to access a network",
			Value:       []string{c.Network.Type},
		})
	}
	for _, mount := range c.Mounts {
		if mount.Source != nil {
			privileges = append(privileges, types.PluginPrivilege{
				Name:        "mount",
				Description: "host path to mount",
				Value:       []string{*mount.Source},
			})
		}
	}
	for _, device := range c.Linux.Devices {
		if device.Path != nil {
			privileges = append(privileges, types.PluginPrivilege{
				Name:        "device",
				Description: "host device to access",
				Value:       []string{*device.Path},
			})
		}
	}
	if c.Linux.DeviceCreation {
		privileges = append(privileges, types.PluginPrivilege{
			Name:        "device-creation",
			Description: "allow creating devices inside plugin",
			Value:       []string{"true"},
		})
	}
	if len(c.Linux.Capabilities) > 0 {
		privileges = append(privileges, types.PluginPrivilege{
			Name:        "capabilities",
			Description: "list of additional capabilities required",
			Value:       c.Linux.Capabilities,
		})
	}

	return privileges, nil
}

// Privileges pulls a plugin config and computes the privileges required to install it.
func (pm *Manager) Privileges(name string, metaHeader http.Header, authConfig *types.AuthConfig) (types.PluginPrivileges, error) {
	_, pd, err := pm.pull(name, metaHeader, authConfig)
	if err != nil {
		return nil, err
	}
	return computePrivileges(pd)
}

// Pull pulls a plugin, check if the correct privileges are provided and install the plugin.
func (pm *Manager) Pull(name string, metaHeader http.Header, authConfig *types.AuthConfig, privileges types.PluginPrivileges) (err error) {
	ref, pd, err := pm.pull(name, metaHeader, authConfig)
	if err != nil {
		return err
	}

	requiredPrivileges, err := computePrivileges(pd)
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(privileges, requiredPrivileges) {
		return errors.New("incorrect privileges")
	}

	pluginID := stringid.GenerateNonCryptoID()
	pluginDir := filepath.Join(pm.config.Root, pluginID)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		logrus.Debugf("error in MkdirAll: %v", err)
		return err
	}

	defer func() {
		if err != nil {
			if delErr := os.RemoveAll(pluginDir); delErr != nil {
				logrus.Warnf("unable to remove %q from failed plugin pull: %v", pluginDir, delErr)
			}
		}
	}()

	err = distribution.WritePullData(pd, filepath.Join(pm.config.Root, pluginID), true)
	if err != nil {
		logrus.Debugf("error in distribution.WritePullData(): %v", err)
		return err
	}

	tag := distribution.GetTag(ref)
	p := NewPlugin(ref.Name(), pluginID, pm.config.ExecRoot, pm.config.Root, tag)
	err = p.InitPlugin()
	if err != nil {
		return err
	}
	pm.config.Store.Add(p)

	pm.config.LogPluginEvent(pluginID, ref.String(), "pull")

	return nil
}

// List displays the list of plugins and associated metadata.
func (pm *Manager) List() ([]types.Plugin, error) {
	plugins := pm.config.Store.GetAll()
	out := make([]types.Plugin, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.PluginObj)
	}
	return out, nil
}

// Push pushes a plugin to the store.
func (pm *Manager) Push(name string, metaHeader http.Header, authConfig *types.AuthConfig) error {
	p, err := pm.config.Store.GetByName(name)
	if err != nil {
		return err
	}
	dest := filepath.Join(pm.config.Root, p.GetID())
	config, err := ioutil.ReadFile(filepath.Join(dest, "config.json"))
	if err != nil {
		return err
	}

	var dummy types.Plugin
	err = json.Unmarshal(config, &dummy)
	if err != nil {
		return err
	}

	rootfs, err := archive.Tar(p.Rootfs, archive.Gzip)
	if err != nil {
		return err
	}
	defer rootfs.Close()

	_, err = distribution.Push(name, pm.config.RegistryService, metaHeader, authConfig, ioutil.NopCloser(bytes.NewReader(config)), rootfs)
	// XXX: Ignore returning digest for now.
	// Since digest needs to be written to the ProgressWriter.
	return err
}

// Remove deletes plugin's root directory.
func (pm *Manager) Remove(name string, config *types.PluginRmConfig) (err error) {
	p, err := pm.config.Store.GetByName(name)
	pm.mu.RLock()
	c := pm.cMap[p]
	pm.mu.RUnlock()

	if err != nil {
		return err
	}

	if !config.ForceRemove {
		if p.GetRefCount() > 0 {
			return fmt.Errorf("plugin %s is in use", p.Name())
		}
		if p.IsEnabled() {
			return fmt.Errorf("plugin %s is enabled", p.Name())
		}
	}

	if p.IsEnabled() {
		if err := pm.disable(p, c); err != nil {
			logrus.Errorf("failed to disable plugin '%s': %s", p.Name(), err)
		}
	}

	id := p.GetID()
	pluginDir := filepath.Join(pm.config.Root, id)

	defer func() {
		if err == nil || config.ForceRemove {
			pm.config.Store.Remove(p)
			pm.config.LogPluginEvent(id, name, "remove")
		}
	}()

	if err = os.RemoveAll(pluginDir); err != nil {
		return errors.Wrap(err, "failed to remove plugin directory")
	}
	return nil
}

// Set sets plugin args
func (pm *Manager) Set(name string, args []string) error {
	p, err := pm.config.Store.GetByName(name)
	if err != nil {
		return err
	}
	return p.Set(args)
}

// CreateFromContext creates a plugin from the given pluginDir which contains
// both the rootfs and the config.json and a repoName with optional tag.
func (pm *Manager) CreateFromContext(ctx context.Context, tarCtx io.ReadCloser, options *types.PluginCreateOptions) error {
	repoName := options.RepoName
	ref, err := distribution.GetRef(repoName)
	if err != nil {
		return err
	}

	name := ref.Name()
	tag := distribution.GetTag(ref)
	pluginID := stringid.GenerateNonCryptoID()

	tmpRootfsDir, err := ioutil.TempDir(pm.tmpDir(), ".rootfs")
	if err != nil {
		return errors.Wrap(err, "failed to create temp directory")
	}
	var configJSON []byte
	rootfs := splitConfigRootFSFromTar(tarCtx, &configJSON)

	rootFSBlob, err := pm.blobStore.New()
	if err != nil {
		return err
	}
	defer rootFSBlob.Close()
	gzw := gzip.NewWriter(rootFSBlob)
	layerDigester := digest.Canonical.New()
	rootfsReader := io.TeeReader(rootfs, io.MultiWriter(gzw, layerDigester.Hash()))

	if err := chrootarchive.Untar(rootfsReader, tmpRootfsDir, nil); err != nil {
		return err
	}
	if err := rootfs.Close(); err != nil {
		return err
	}

	if configJSON == nil {
		return errors.New("config not found")
	}

	if err := gzw.Close(); err != nil {
		return errors.Wrap(err, "error closing gzip writer")
	}

	var config types.PluginConfig
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	if err := pm.validateConfig(config); err != nil {
		return err
	}

	rootFSBlobsum, err := rootFSBlob.Commit()
	if err != nil {
		return err
	}

	config.Rootfs = &types.PluginConfigRootfs{
		Type:    "layers",
		DiffIds: []string{layerDigester.Digest().String()},
	}

	configBlob, err := pm.blobStore.New()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(configBlob).Encode(config); err != nil {
		return errors.Wrap(err, "error encoding json config")
	}
	configBlobsum, err := configBlob.Commit()
	if err != nil {
		return err
	}

	logrus.Debugf("rootfs gzip blob: %v", rootFSBlobsum)
	logrus.Debugf("rootfs diffid: %v", layerDigester.Digest())
	logrus.Debugf("config: %s %v", config)
	logrus.Debugf("config blobsum: %v", configBlobsum)
	logrus.Debugf("rootfs temporarily extracted to: %v", tmpRootfsDir)

	// pm.newPlugin(config, tmpRootfsDir)

	return nil

	p := NewPlugin(name, pluginID, pm.config.ExecRoot, pm.config.Root, tag)

	if v, _ := pm.config.Store.GetByName(p.Name()); v != nil { // TODO: this is not safe
		return fmt.Errorf("plugin %q already exists", p.Name())
	}

	pluginDir := filepath.Join(pm.config.Root, pluginID)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return err
	}

	// In case an error happens, remove the created directory.
	if err := pm.createFromContext(ctx, tarCtx, pluginDir, repoName, p); err != nil {
		if err := os.RemoveAll(pluginDir); err != nil {
			logrus.Warnf("unable to remove %q from failed plugin creation: %v", pluginDir, err)
		}
		return err
	}

	return nil
}

func (pm *Manager) validateConfig(config types.PluginConfig) error {
	return nil // TODO:
}

func splitConfigRootFSFromTar(in io.ReadCloser, config *[]byte) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		tarReader := tar.NewReader(in)
		tarWriter := tar.NewWriter(pw)
		defer in.Close()

		hasRootfs := false

		for {
			hdr, err := tarReader.Next()
			if err == io.EOF {
				if !hasRootfs {
					pw.CloseWithError(errors.Wrap(err, "no rootfs found"))
					return
				}
				// Signals end of archive.
				tarWriter.Close()
				pw.Close()
				return
			}
			if err != nil {
				pw.CloseWithError(errors.Wrap(err, "failed to read from tar"))
				return
			}

			content := io.Reader(tarReader)
			name := filepath.Clean(hdr.Name)
			if filepath.IsAbs(name) {
				name = name[1:]
			}
			if name == configFileName {
				dt, err := ioutil.ReadAll(content)
				if err != nil {
					pw.CloseWithError(errors.Wrap(err, "failed to read config.json"))
					return
				}
				*config = dt
			}
			if parts := strings.Split(name, string(filepath.Separator)); len(parts) != 0 && parts[0] == rootfsFileName {
				hdr.Name = filepath.Clean(filepath.Join(parts[1:]...))
				if err := tarWriter.WriteHeader(hdr); err != nil {
					pw.CloseWithError(errors.Wrap(err, "error writing tar header"))
					return
				}
				if _, err := pools.Copy(tarWriter, content); err != nil {
					pw.CloseWithError(errors.Wrap(err, "error copying tar data"))
					return
				}
				hasRootfs = true
			} else {
				io.Copy(ioutil.Discard, content)
			}
		}
	}()
	return pr
}

func (pm *Manager) createFromContext(ctx context.Context, tarCtx io.Reader, pluginDir, repoName string, p *Plugin) error {
	if err := chrootarchive.Untar(tarCtx, pluginDir, nil); err != nil {
		return err
	}

	if err := p.InitPlugin(); err != nil {
		return err
	}

	if err := pm.config.Store.Add(p); err != nil {
		return err
	}

	pm.config.LogPluginEvent(p.GetID(), repoName, "create")

	return nil
}

func getPluginName(name string) (string, error) {
	named, err := reference.ParseNamed(name) // FIXME: validate
	if err != nil {
		return "", err
	}
	if reference.IsNameOnly(named) {
		named = reference.WithDefaultTag(named)
	}
	ref, ok := named.(reference.NamedTagged)
	if !ok {
		return "", fmt.Errorf("invalid name: %s", named.String())
	}
	return ref.String(), nil
}

type basicBlobStore struct {
	path string
}

func newBasicBlobStore(p string) (*basicBlobStore, error) {
	tmpdir := filepath.Join(p, "_tmp")
	if err := os.MkdirAll(tmpdir, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to mkdir %v", p)
	}
	return &basicBlobStore{path: p}, nil
}

func (b *basicBlobStore) New() (WriteCommitCloser, error) {
	f, err := ioutil.TempFile(filepath.Join(b.path, "_tmp"), ".insertion")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temp file")
	}
	return newInsertion(f), nil
}

func (b *basicBlobStore) Get(dgst digest.Digest) (io.ReadCloser, error) {
	return os.Open(filepath.Join(b.path, string(dgst.Algorithm()), dgst.Hex()))
}

type WriteCommitCloser interface {
	io.WriteCloser
	Commit() (digest.Digest, error)
}

type insertion struct {
	io.Writer
	f        *os.File
	digester digest.Digester
	closed   bool
}

func newInsertion(tempFile *os.File) *insertion {
	digester := digest.Canonical.New()
	return &insertion{f: tempFile, digester: digester, Writer: io.MultiWriter(tempFile, digester.Hash())}
}

func (i *insertion) Commit() (digest.Digest, error) {
	p := i.f.Name()
	d := filepath.Join(filepath.Join(p, "../../"))
	i.f.Sync()
	defer os.RemoveAll(p)
	if err := i.f.Close(); err != nil {
		return "", err
	}
	i.closed = true
	dgst := i.digester.Digest()
	if err := os.MkdirAll(filepath.Join(d, string(dgst.Algorithm())), 0700); err != nil {
		return "", errors.Wrapf(err, "failed to mkdir %v", d)
	}
	if err := os.Rename(p, filepath.Join(d, string(dgst.Algorithm()), dgst.Hex())); err != nil {
		return "", errors.Wrapf(err, "failed to rename %v", p)
	}
	return dgst, nil
}

func (i *insertion) Close() error {
	if i.closed {
		return nil
	}
	defer os.RemoveAll(i.f.Name())
	return i.f.Close()
}
