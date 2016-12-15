// +build linux

package plugin

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/docker/api/types"
	dockerdist "github.com/docker/docker/distribution"
	"github.com/docker/docker/distribution/xfer"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/pools"
	"github.com/docker/docker/pkg/progress"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/reference"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

var (
	validFullID = regexp.MustCompile(`^([a-f0-9]{64})$`)
)

// Disable deactivates a plugin. This means resources (volumes, networks) cant use them.
func (pm *Manager) Disable(refOrID string, config *types.PluginDisableConfig) error {
	p, err := pm.config.Store.GetV2Plugin(refOrID)
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
	pm.config.LogPluginEvent(p.GetID(), refOrID, "disable")
	return nil
}

// Enable activates a plugin, which implies that they are ready to be used by containers.
func (pm *Manager) Enable(refOrID string, config *types.PluginEnableConfig) error {
	p, err := pm.config.Store.GetV2Plugin(refOrID)
	if err != nil {
		return err
	}

	c := &controller{timeoutInSecs: config.Timeout}
	if err := pm.enable(p, c, false); err != nil {
		return err
	}
	pm.config.LogPluginEvent(p.GetID(), refOrID, "enable")
	return nil
}

// Inspect examines a plugin config
func (pm *Manager) Inspect(refOrID string) (tp *types.Plugin, err error) {
	p, err := pm.config.Store.GetV2Plugin(refOrID)
	if err != nil {
		return nil, err
	}

	return &p.PluginObj, nil
}

func (pm *Manager) pull(ctx context.Context, name string, config *dockerdist.ImagePullConfig, outStream io.Writer) error {
	ref, err := reference.ParseNamed(name) // fix pull-by-digest
	if err != nil {
		return err
	}
	name = reference.WithDefaultTag(ref).String()
	if err := pm.config.Store.validateName(name); err != nil {
		return err
	}

	if outStream != nil {
		// Include a buffer so that slow client connections don't affect
		// transfer performance.
		progressChan := make(chan progress.Progress, 100)

		writesDone := make(chan struct{})

		defer func() {
			close(progressChan)
			<-writesDone
		}()

		var cancelFunc context.CancelFunc
		ctx, cancelFunc = context.WithCancel(ctx)

		go func() {
			writeDistributionProgress(cancelFunc, outStream, progressChan)
			close(writesDone)
		}()

		config.ProgressOutput = progress.ChanOutput(progressChan)
	} else {
		config.ProgressOutput = progress.DiscardOutput()
	}
	return dockerdist.Pull(ctx, ref, config)
}

type tempConfigStore struct {
	config       []byte
	configDigest digest.Digest
}

func (s *tempConfigStore) Put(c []byte) (digest.Digest, error) {
	dgst := digest.FromBytes(c)

	s.config = c
	s.configDigest = dgst

	return dgst, nil
}

func (s *tempConfigStore) Get(d digest.Digest) ([]byte, error) {
	if d != s.configDigest {
		return nil, digest.ErrDigestNotFound
	}
	return s.config, nil
}

func (s *tempConfigStore) RootFSFromConfig(c []byte) (*image.RootFS, error) {
	return configToRootFS(c)
}

func writeDistributionProgress(cancelFunc func(), outStream io.Writer, progressChan <-chan progress.Progress) {
	progressOutput := streamformatter.NewJSONStreamFormatter().NewProgressOutput(outStream, false)
	operationCancelled := false

	for prog := range progressChan {
		if err := progressOutput.WriteProgress(prog); err != nil && !operationCancelled {
			// don't log broken pipe errors as this is the normal case when a client aborts
			if isBrokenPipe(err) {
				logrus.Info("Pull session cancelled")
			} else {
				logrus.Errorf("error writing progress to client: %v", err)
			}
			cancelFunc()
			operationCancelled = true
			// Don't return, because we need to continue draining
			// progressChan until it's closed to avoid a deadlock.
		}
	}
}

func isBrokenPipe(e error) bool {
	if netErr, ok := e.(*net.OpError); ok {
		e = netErr.Err
		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			e = sysErr.Err
		}
	}
	return e == syscall.EPIPE
}

func computePrivileges(c types.PluginConfig) (types.PluginPrivileges, error) {
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
func (pm *Manager) Privileges(ctx context.Context, name string, metaHeader http.Header, authConfig *types.AuthConfig) (types.PluginPrivileges, error) {
	// create image store instance
	cs := &tempConfigStore{}

	pluginPullConfig := &dockerdist.ImagePullConfig{
		Config: dockerdist.Config{
			MetaHeaders:      metaHeader,
			AuthConfig:       authConfig,
			RegistryService:  pm.config.RegistryService,
			ImageEventLogger: func(string, string, string) {},
			ImageStore:       cs,
		},
		Schema2Types: dockerdist.PluginTypes,
	}

	if err := pm.pull(ctx, name, pluginPullConfig, nil); err != nil {
		return nil, err
	}

	if cs.config == nil {
		return nil, errors.New("no configuration pulled")
	}
	var config types.PluginConfig
	if err := json.Unmarshal(cs.config, &config); err != nil {
		return nil, err
	}

	return computePrivileges(config)
}

// Pull pulls a plugin, check if the correct privileges are provided and install the plugin.
func (pm *Manager) Pull(ctx context.Context, name string, metaHeader http.Header, authConfig *types.AuthConfig, privileges types.PluginPrivileges, outStream io.Writer) (err error) {
	tmpRootfsDir, err := ioutil.TempDir(pm.tmpDir(), ".rootfs")
	defer os.RemoveAll(tmpRootfsDir)

	dm := &downloadManager{
		tmpDir:    tmpRootfsDir,
		blobStore: pm.blobStore,
	}

	pluginPullConfig := &dockerdist.ImagePullConfig{
		Config: dockerdist.Config{
			MetaHeaders:      metaHeader,
			AuthConfig:       authConfig,
			RegistryService:  pm.config.RegistryService,
			ImageEventLogger: pm.config.LogPluginEvent,
			ImageStore:       dm,
		},
		DownloadManager: dm,
		Schema2Types:    dockerdist.PluginTypes,
	}

	err = pm.pull(ctx, name, pluginPullConfig, outStream)
	if err != nil {
		return err
	}

	if _, err := pm.createPlugin(name, dm.configDigest, dm.blobs, tmpRootfsDir, &privileges); err != nil {
		// todo: clear unused blobs
		return err
	}

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
func (pm *Manager) Push(ctx context.Context, name string, metaHeader http.Header, authConfig *types.AuthConfig, outStream io.Writer) error {
	p, err := pm.config.Store.GetV2Plugin(name)
	if err != nil {
		return err
	}

	ref, err := reference.ParseNamed(p.Name())
	if err != nil {
		return errors.Wrapf(err, "plugin has invalid name %v for push", p.Name())
	}

	var po progress.Output
	if outStream != nil {
		// Include a buffer so that slow client connections don't affect
		// transfer performance.
		progressChan := make(chan progress.Progress, 100)

		writesDone := make(chan struct{})

		defer func() {
			close(progressChan)
			<-writesDone
		}()

		var cancelFunc context.CancelFunc
		ctx, cancelFunc = context.WithCancel(ctx)

		go func() {
			writeDistributionProgress(cancelFunc, outStream, progressChan)
			close(writesDone)
		}()

		po = progress.ChanOutput(progressChan)
	} else {
		po = progress.DiscardOutput()
	}

	// TODO: replace these with manager
	is := &pluginConfigStore{
		pm:     pm,
		plugin: p,
	}
	ls := &pluginLayerProvider{
		pm:     pm,
		plugin: p,
	}
	rs := &pluginReference{
		name:     ref,
		pluginID: p.Config,
	}

	uploadManager := xfer.NewLayerUploadManager(3)

	imagePushConfig := &dockerdist.ImagePushConfig{
		Config: dockerdist.Config{
			MetaHeaders:      metaHeader,
			AuthConfig:       authConfig,
			ProgressOutput:   po,
			RegistryService:  pm.config.RegistryService,
			ReferenceStore:   rs,
			ImageEventLogger: pm.config.LogPluginEvent,
			ImageStore:       is,
			RequireSchema2:   true,
		},
		ConfigMediaType: schema2.MediaTypePluginConfig,
		LayerStore:      ls,
		UploadManager:   uploadManager,
	}

	return dockerdist.Push(ctx, ref, imagePushConfig)
}

type pluginReference struct {
	name     reference.Named
	pluginID digest.Digest
}

func (r *pluginReference) References(id digest.Digest) []reference.Named {
	if r.pluginID != id {
		return nil
	}
	return []reference.Named{r.name}
}

func (r *pluginReference) ReferencesByName(ref reference.Named) []reference.Association {
	return []reference.Association{
		{
			Ref: r.name,
			ID:  r.pluginID,
		},
	}
}

func (r *pluginReference) Get(ref reference.Named) (digest.Digest, error) {
	if r.name.String() != ref.String() {
		return digest.Digest(""), reference.ErrDoesNotExist
	}
	return r.pluginID, nil
}

func (r *pluginReference) AddTag(ref reference.Named, id digest.Digest, force bool) error {
	// Read only, ignore
	return nil
}
func (r *pluginReference) AddDigest(ref reference.Canonical, id digest.Digest, force bool) error {
	// Read only, ignore
	return nil
}
func (r *pluginReference) Delete(ref reference.Named) (bool, error) {
	// Read only, ignore
	return false, nil
}

type pluginConfigStore struct {
	pm     *Manager
	plugin *Plugin
}

func (s *pluginConfigStore) Put([]byte) (digest.Digest, error) {
	return digest.Digest(""), errors.New("cannot store config on push")
}

func (s *pluginConfigStore) Get(d digest.Digest) ([]byte, error) {
	if s.plugin.Config != d {
		return nil, errors.New("plugin not found")
	}
	rwc, err := s.pm.blobStore.Get(d)
	if err != nil {
		return nil, err
	}
	defer rwc.Close()
	return ioutil.ReadAll(rwc)
}

func (s *pluginConfigStore) RootFSFromConfig(c []byte) (*image.RootFS, error) {
	return configToRootFS(c)
}

type pluginLayerProvider struct {
	pm     *Manager
	plugin *Plugin
}

func configToRootFS(c []byte) (*image.RootFS, error) {
	var pluginConfig types.PluginConfig
	if err := json.Unmarshal(c, &pluginConfig); err != nil {
		return nil, err
	}

	return rootFSFromPlugin(pluginConfig.Rootfs), nil
}

func rootFSFromPlugin(pluginfs *types.PluginConfigRootfs) *image.RootFS {
	rootfs := image.RootFS{
		Type:    pluginfs.Type,
		DiffIDs: make([]layer.DiffID, len(pluginfs.DiffIds)),
	}
	for i := range pluginfs.DiffIds {
		rootfs.DiffIDs[i] = layer.DiffID(pluginfs.DiffIds[i])
	}

	return &rootfs
}

func (p *pluginLayerProvider) Get(id layer.ChainID) (dockerdist.PushLayer, error) {
	rootfs := rootFSFromPlugin(p.plugin.PluginObj.Config.Rootfs)
	var i int
	for i = 0; i < len(rootfs.DiffIDs); i++ {
		if layer.CreateChainID(rootfs.DiffIDs[i:]) == id {
			break
		}
	}
	if i == len(rootfs.DiffIDs) {
		return nil, errors.New("layer not found")
	}
	return &pluginLayer{
		pm:      p.pm,
		diffIDs: rootfs.DiffIDs[i:],
		blobs:   p.plugin.Blobsums[i:],
	}, nil
}

type pluginLayer struct {
	pm      *Manager
	diffIDs []layer.DiffID
	blobs   []digest.Digest
}

func (l *pluginLayer) ChainID() layer.ChainID {
	return layer.CreateChainID(l.diffIDs)
}

func (l *pluginLayer) DiffID() layer.DiffID {
	return l.diffIDs[0]
}

func (l *pluginLayer) Parent() dockerdist.PushLayer {
	if len(l.diffIDs) == 1 {
		return nil
	}
	return &pluginLayer{
		pm:      l.pm,
		diffIDs: l.diffIDs[1:],
		blobs:   l.blobs[1:],
	}
}

func (l *pluginLayer) Open() (io.ReadCloser, error) {
	return l.pm.blobStore.Get(l.blobs[0])
}

func (l *pluginLayer) Size() int64 {
	size, _ := l.pm.blobStore.Size(l.blobs[0])
	return size
}

func (l *pluginLayer) MediaType() string {
	return schema2.MediaTypeLayer
}

func (l *pluginLayer) Release() {
	// Nothing needs to be release, no references held
}

// Remove deletes plugin's root directory.
func (pm *Manager) Remove(name string, config *types.PluginRmConfig) (err error) {
	p, err := pm.config.Store.GetV2Plugin(name)
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
	p, err := pm.config.Store.GetV2Plugin(name)
	if err != nil {
		return err
	}
	if err := p.Set(args); err != nil {
		return err
	}
	return pm.save(p)
}

// CreateFromContext creates a plugin from the given pluginDir which contains
// both the rootfs and the config.json and a repoName with optional tag.
func (pm *Manager) CreateFromContext(ctx context.Context, tarCtx io.ReadCloser, options *types.PluginCreateOptions) error {
	ref, err := reference.ParseNamed(options.RepoName)
	if err != nil {
		return errors.Wrapf(err, "failed to parse reference %v", options.RepoName)
	}
	if _, ok := ref.(reference.Canonical); ok {
		return errors.Errorf("canonical references are not permitted")
	}
	name := reference.WithDefaultTag(ref).String()

	if err := pm.config.Store.validateName(name); err != nil { // fast check, real check is in createPlugin()
		return err
	}

	tmpRootfsDir, err := ioutil.TempDir(pm.tmpDir(), ".rootfs")
	defer os.RemoveAll(tmpRootfsDir)
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

	pm.mu.Lock()
	defer pm.mu.Unlock()

	var cleanupBlobs []digest.Digest
	defer func() {
		pm.cleanupUnusedBlobs(cleanupBlobs...)
	}()

	rootFSBlobsum, err := rootFSBlob.Commit()
	if err != nil {
		return err
	}
	cleanupBlobs = append(cleanupBlobs, rootFSBlobsum)

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
	cleanupBlobs = append(cleanupBlobs, configBlobsum)

	p, err := pm.createPlugin(name, configBlobsum, []digest.Digest{rootFSBlobsum}, tmpRootfsDir, nil)
	if err == nil {
		cleanupBlobs = nil
	}

	pm.config.LogPluginEvent(p.PluginObj.ID, name, "create")

	return err
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
