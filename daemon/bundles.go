package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"sort"
	"time"

	"github.com/docker/distribution/digest"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/distribution"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/image"
	"github.com/docker/docker/image/bundle"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/httputils"
	"github.com/docker/docker/pkg/progress"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/reference"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

var acceptedBundleFilterTags = map[string]bool{
	"label":  true,
	"before": true,
	"since":  true,
}

// bundleByCreated is a temporary type used to sort a list of bundles by creation
// time.
type bundleByCreated []*types.Bundle

func (r bundleByCreated) Len() int           { return len(r) }
func (r bundleByCreated) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
func (r bundleByCreated) Less(i, j int) bool { return r[i].Created < r[j].Created }

// Bundles returns a filtered list of bundles. filterArgs is a JSON-encoded set
// of filter arguments which will be interpreted by api/types/filters.
// filter is a shell glob string applied to repository names. The argument
// named all controls whether all bundles in the graph are filtered, or just
// the heads.
func (daemon *Daemon) Bundles(filterArgs, filter string) ([]*types.Bundle, error) {
	bundleFilters, err := filters.FromParam(filterArgs)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse filterArgs: %v", filterArgs)
	}
	if err := bundleFilters.Validate(acceptedBundleFilterTags); err != nil {
		return nil, errors.Wrapf(err, "failed to validate filters: %v", bundleFilters)
	}

	var bundles []*types.Bundle

	var beforeFilter, sinceFilter *bundle.Bundle
	err = bundleFilters.WalkValues("before", func(value string) error {
		beforeFilter, err = daemon.GetBundle(value)
		return errors.Wrapf(err, "failed to get bundle for before filter: %v", value)
	})
	if err != nil {
		return nil, err
	}

	err = bundleFilters.WalkValues("since", func(value string) error {
		sinceFilter, err = daemon.GetBundle(value)
		return errors.Wrapf(err, "failed to get bundle for since filter: %v", value)
	})
	if err != nil {
		return nil, err
	}

	var filterTagged bool
	if filter != "" {
		filterRef, err := reference.ParseNamed(filter)
		if err == nil { // parse error means wildcard repo
			if _, ok := filterRef.(reference.NamedTagged); ok {
				filterTagged = true
			}
		}
	}

	for id, b := range daemon.bundleStore.Map() {
		if beforeFilter != nil {
			if b.Created.Equal(beforeFilter.Created) || b.Created.After(beforeFilter.Created) {
				continue
			}
		}

		if sinceFilter != nil {
			if b.Created.Equal(sinceFilter.Created) || b.Created.Before(sinceFilter.Created) {
				continue
			}
		}

		if bundleFilters.Include("label") {
			if !bundleFilters.MatchKVList("label", b.Labels) {
				continue
			}
		}

		newBundle := newAPIBundle(b)

		for _, ref := range daemon.bundleReferenceStore.References(id.Digest()) {
			if filter != "" { // filter by tag/repo name
				if filterTagged { // filter by tag, require full ref match
					if ref.String() != filter {
						continue
					}
				} else if matched, err := path.Match(filter, ref.Name()); !matched || err != nil { // name only match, FIXME: docs say exact
					continue
				}
			}
			if _, ok := ref.(reference.Canonical); ok {
				newBundle.RepoDigests = append(newBundle.RepoDigests, ref.String())
			}
			if _, ok := ref.(reference.NamedTagged); ok {
				newBundle.RepoTags = append(newBundle.RepoTags, ref.String())
			}
		}

		bundles = append(bundles, newBundle)
	}

	sort.Sort(sort.Reverse(bundleByCreated(bundles)))

	return bundles, nil
}

func newAPIBundle(bundle *bundle.Bundle) *types.Bundle {
	newBundle := new(types.Bundle)
	newBundle.ID = bundle.ID().String()
	newBundle.Created = bundle.Created.Unix()
	newBundle.Labels = bundle.Labels
	return newBundle
}

// GetBundleID returns an bundle ID corresponding to the bundle referred to by
// refOrID.
func (daemon *Daemon) GetBundleID(refOrID string) (bundle.ID, error) {
	id, ref, err := reference.ParseIDOrReference(refOrID)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse ID or reference: %v", refOrID)
	}
	if id != "" {
		if _, err := daemon.bundleStore.Get(bundle.ID(id)); err != nil {
			return "", ErrRefDoesNotExist{refOrID}
		}
		return bundle.ID(id), nil
	}

	if id, err := daemon.bundleReferenceStore.Get(ref); err == nil {
		return bundle.ID(id), nil
	}
	if tagged, ok := ref.(reference.NamedTagged); ok {
		if id, err := daemon.bundleStore.Search(tagged.Tag()); err == nil {
			for _, namedRef := range daemon.bundleReferenceStore.References(id.Digest()) {
				if namedRef.Name() == ref.Name() {
					return id, nil
				}
			}
		}
	}

	// Search based on ID
	if id, err := daemon.bundleStore.Search(refOrID); err == nil {
		return id, nil
	}

	return "", ErrRefDoesNotExist{refOrID}
}

// GetBundle returns an bundle corresponding to the bundle referred to by refOrID.
func (daemon *Daemon) GetBundle(refOrID string) (*bundle.Bundle, error) {
	imgID, err := daemon.GetBundleID(refOrID)
	if err != nil {
		return nil, err
	}
	return daemon.bundleStore.Get(imgID)
}

// LookupBundle looks up an Bundle by name and returns it as an BundleInspect
// structure.
func (daemon *Daemon) LookupBundle(name string) (*types.BundleInspect, error) {
	bundle, err := daemon.GetBundle(name)
	if err != nil {
		return nil, fmt.Errorf("no such bundle: %s", name)
	}

	repoTags, repoDigests := splitReferencesByType(daemon.bundleReferenceStore.References(bundle.ID().Digest()))

	bundleInspect := &types.BundleInspect{
		ID:            bundle.ID().String(),
		RepoTags:      repoTags,
		RepoDigests:   repoDigests,
		Created:       bundle.Created.Format(time.RFC3339Nano),
		DockerVersion: bundle.DockerVersion,
	}

	for _, s := range bundle.Services {
		img, err := daemon.LookupImage(string(s.Image))
		if err != nil {
			return nil, err
		}
		sInspect := &types.BundleService{
			Name:  s.Name,
			Image: img,
		}
		bundleInspect.Services = append(bundleInspect.Services, sInspect)
	}

	return bundleInspect, nil
}

// CreateBundle creates a new bundle from a configuration. Configuration may come from external source.
func (daemon *Daemon) CreateBundle(src, repository, tag string, inConfig io.ReadCloser, outStream io.Writer) error {
	var (
		sf     = streamformatter.NewJSONStreamFormatter()
		rc     io.ReadCloser
		resp   *http.Response
		newRef reference.Named
	)

	if repository != "" {
		var err error
		newRef, err = reference.ParseNamed(repository)
		if err != nil {
			return errors.Wrapf(err, "failed to parse %v", repository)
		}

		if _, isCanonical := newRef.(reference.Canonical); isCanonical {
			return errors.New("cannot create a digest reference")
		}

		if tag != "" {
			newRef, err = reference.WithTag(newRef, tag)
			if err != nil {
				return errors.Wrapf(err, "failed to create a tag reference %v %v", newRef, tag)
			}
		}
	}

	if src == "-" {
		rc = inConfig
	} else {
		inConfig.Close()
		u, err := url.Parse(src)
		if err != nil {
			return err
		}
		if u.Scheme == "" {
			u.Scheme = "http"
			u.Host = src
			u.Path = ""
		}
		outStream.Write(sf.FormatStatus("", "Downloading from %s", u))
		resp, err = httputils.Download(u.String())
		if err != nil {
			return errors.Wrapf(err, "failed to download config from %v", u.String())
		}
		progressOutput := sf.NewProgressOutput(outStream, true)
		rc = progress.NewProgressReader(resp.Body, progressOutput, resp.ContentLength, "", "Importing")
	}
	defer rc.Close()

	inflatedData, err := archive.DecompressStream(rc)
	if err != nil {
		return errors.Wrap(err, "failed to depcompress config")
	}

	config, err := ioutil.ReadAll(inflatedData)
	if err != nil {
		return errors.Wrap(err, "failed to read config data")
	}

	b, err := bundle.NewFromJSON(config)
	if err != nil {
		return errors.Wrapf(err, "failed to parse bundle config %v", string(config))
	}

	for _, s := range b.Services {
		if _, err := daemon.imageStore.Get(s.Image); err != nil {
			return errors.Wrapf(err, "failed to get image %v referenced by bundle configuration", s.Image)
		}
	}

	remarshal := false

	if b.Created == (time.Time{}) {
		remarshal = true
		b.Created = time.Now().UTC()
	}

	if b.DockerVersion == "" {
		remarshal = true
		b.DockerVersion = dockerversion.Version
	}

	if remarshal {
		config, err = json.Marshal(b)
		if err != nil {
			return errors.Wrap(err, "failed to remarshal bundle config")
		}
	}

	id, err := daemon.bundleStore.Create(config)
	if err != nil {
		return errors.Wrapf(err, "failed to create bundle: %v", string(config))
	}

	if newRef != nil {
		if err := daemon.TagBundleWithReference(id, newRef); err != nil {
			return err
		}
	}

	outStream.Write(sf.FormatStatus("", id.String()))

	return nil
}

// TagBundle creates the tag specified by newTag, pointing to the bundle named
// bundleName (alternatively, bundleName can also be an bundle ID).
func (daemon *Daemon) TagBundle(bundleName, repository, tag string) error {
	bundleID, err := daemon.GetBundleID(bundleName)
	if err != nil {
		return err
	}

	newTag, err := reference.WithName(repository)
	if err != nil {
		return errors.Wrapf(err, "failed to create reference from name: %v", repository)
	}
	if tag != "" {
		if newTag, err = reference.WithTag(newTag, tag); err != nil {
			return errors.Wrapf(err, "failed to create a tag reference from %v %v", newTag, tag)
		}
	}

	return daemon.TagBundleWithReference(bundleID, newTag)
}

// TagBundleWithReference adds the given reference to the bundle ID provided.
func (daemon *Daemon) TagBundleWithReference(bundleID bundle.ID, newTag reference.Named) error {
	if err := daemon.bundleReferenceStore.AddTag(newTag, bundleID.Digest(), true); err != nil {
		return errors.Wrapf(err, "failed to tag a bundle %v to %v", bundleID, newTag)
	}

	return nil
}

// PushBundle pushes bundle to the registry.
func (daemon *Daemon) PushBundle(ctx context.Context, repo, tag string, metaHeaders map[string][]string, authConfig *types.AuthConfig, outStream io.Writer) error {
	ref, err := reference.ParseNamed(repo)
	if err != nil {
		return errors.Wrapf(err, "failed to create reference from name: %v", repo)
	}
	if tag != "" {
		// Push by digest is not supported, so only tags are supported.
		ref, err = reference.WithTag(ref, tag)
		if err != nil {
			return errors.Wrapf(err, "failed to create a tag reference from %v %v", ref, tag)
		}
	}

	// Include a buffer so that slow client connections don't affect
	// transfer performance.
	progressChan := make(chan progress.Progress, 100)

	writesDone := make(chan struct{})

	ctx, cancelFunc := context.WithCancel(ctx)

	go func() {
		writeDistributionProgress(cancelFunc, outStream, progressChan)
		close(writesDone)
	}()

	pushConfig := &distribution.PushConfig{
		MetaHeaders:     metaHeaders,
		AuthConfig:      authConfig,
		ProgressOutput:  progress.ChanOutput(progressChan),
		RegistryService: daemon.RegistryService,
		EventLogger:     daemon.logBundleEvent,
		MetadataStore:   daemon.distributionMetadataStore,
		LayerStore:      daemon.layerStore,
		ImageStore:      daemon.imageStore,
		ReferenceStore:  daemon.bundleReferenceStore,
		TrustKey:        daemon.trustKey,
		UploadManager:   daemon.uploadManager,
		BundleStore:     daemon.bundleStore,
	}

	err = distribution.Push(ctx, ref, pushConfig)
	close(progressChan)
	<-writesDone
	return err
}

// BundleDelete deletes bundle. On deletion by reference, if bundle has multiple
// references, only the reference is untagged
func (daemon *Daemon) BundleDelete(bundleRef string, force, prune bool) ([]types.BundleDelete, error) {
	bundleID, err := daemon.GetBundleID(bundleRef)
	if err != nil {
		return nil, daemon.refNotExistToErrcode("bundle", err)
	}

	repoRefs := daemon.bundleReferenceStore.References(bundleID.Digest())
	repoTags, _ := splitReferencesByType(repoRefs)

	if len(repoTags) > 1 && !force {
		ref, err := reference.ParseNamed(bundleRef)
		if err != nil {
			return nil, errors.Errorf("failed to remove bundle with multiple references")
		}
		repoRefs = []reference.Named{ref}
	}

	for _, r := range repoRefs {
		if _, err := daemon.bundleReferenceStore.Delete(r); err != nil {
			return nil, errors.Wrapf(err, "failed to untag %v", r)
		}
	}

	_, err = daemon.bundleStore.Delete(bundleID)
	// todo: delete unreferences images here?
	return nil, errors.Wrapf(err, "failed to remove bundle %v", bundleID)
}

// PullBundle initiates a pull operation. bundle is the repository name to pull, and
// tag may be either empty, or indicate a specific tag to pull.
func (daemon *Daemon) PullBundle(ctx context.Context, bundle, tag string, metaHeaders map[string][]string, authConfig *types.AuthConfig, outStream io.Writer) error {
	ref, err := reference.ParseNamed(bundle)
	if err != nil {
		return err
	}

	if tag != "" {
		// The "tag" could actually be a digest.
		var dgst digest.Digest
		dgst, err = digest.ParseDigest(tag)
		if err == nil {
			ref, err = reference.WithDigest(ref, dgst)
		} else {
			ref, err = reference.WithTag(ref, tag)
		}
		if err != nil {
			return err
		}
	}

	// Include a buffer so that slow client connections don't affect
	// transfer performance.
	progressChan := make(chan progress.Progress, 100)

	writesDone := make(chan struct{})

	ctx, cancelFunc := context.WithCancel(ctx)

	go func() {
		writeDistributionProgress(cancelFunc, outStream, progressChan)
		close(writesDone)
	}()

	pullConfig := &distribution.PullConfig{
		MetaHeaders:     metaHeaders,
		AuthConfig:      authConfig,
		ProgressOutput:  progress.ChanOutput(progressChan),
		RegistryService: daemon.RegistryService,
		EventLogger:     daemon.logBundleEvent,
		MetadataStore:   daemon.distributionMetadataStore,
		ImageStore:      daemon.imageStore,
		BundleStore:     daemon.bundleStore,
		ReferenceStore:  daemon.bundleReferenceStore,
		DownloadManager: daemon.downloadManager,
	}

	err = distribution.Pull(ctx, ref, pullConfig)
	close(progressChan)
	<-writesDone
	return err
}

// ResolveBundleManifest returnes a bundle by a reference. Reference is first
// pulled from registry, if this fails local bundle definition will be returned.
func (daemon *Daemon) ResolveBundleManifest(bundleRef string, authConfig *types.AuthConfig) (*bundle.Bundle, error) {
	ref, err := reference.ParseNamed(bundleRef)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse reference %v", bundleRef)
	}

	selector := &bundleImageSelector{} // only pull manifest

	pullConfig := &distribution.PullConfig{
		MetaHeaders:         make(map[string][]string),
		AuthConfig:          authConfig,
		ProgressOutput:      progress.Discard,
		RegistryService:     daemon.RegistryService,
		EventLogger:         daemon.logBundleEvent,
		MetadataStore:       daemon.distributionMetadataStore,
		ImageStore:          daemon.imageStore,
		BundleStore:         selector,
		ReferenceStore:      daemon.bundleReferenceStore,
		DownloadManager:     daemon.downloadManager,
		BundleImageSelector: selector,
	}

	err = distribution.Pull(context.Background(), ref, pullConfig)
	if errors.Cause(err) != errBundleStopAfterPull {
		dgst, err := daemon.bundleReferenceStore.Get(ref)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get bundle by reference %v", ref)
		}
		return daemon.bundleStore.Get(bundle.IDFromDigest(dgst))
	}

	return bundle.NewFromJSON(selector.config)
}

// ResolveBundleImage pulls a single image referenced by a bundle from a registry.
// If this fails it will check if a local bundle exists. ResolveBundleImage returns
// image ID of the pulled image.
func (daemon *Daemon) ResolveBundleImage(ctx context.Context, bundleRef, imageName string, metaHeaders map[string][]string, authConfig *types.AuthConfig, outStream io.Writer) (image.ID, error) {
	ref, err := reference.ParseNamed(bundleRef)
	if err != nil {
		return "", errors.Wrapf(err, "error parsing %v", bundleRef)
	}

	selector := &bundleImageSelector{
		name: imageName,
	}

	// Include a buffer so that slow client connections don't affect
	// transfer performance.
	progressChan := make(chan progress.Progress, 100)

	writesDone := make(chan struct{})

	ctx, cancelFunc := context.WithCancel(ctx)

	go func() {
		writeDistributionProgress(cancelFunc, outStream, progressChan)
		close(writesDone)
	}()

	pullConfig := &distribution.PullConfig{
		MetaHeaders:         metaHeaders,
		AuthConfig:          authConfig,
		ProgressOutput:      progress.ChanOutput(progressChan),
		RegistryService:     daemon.RegistryService,
		EventLogger:         daemon.logBundleEvent,
		MetadataStore:       daemon.distributionMetadataStore,
		ImageStore:          daemon.imageStore,
		BundleStore:         selector,
		ReferenceStore:      daemon.bundleReferenceStore,
		DownloadManager:     daemon.downloadManager,
		BundleImageSelector: selector,
	}

	err = distribution.Pull(ctx, ref, pullConfig)
	close(progressChan)
	<-writesDone
	if errors.Cause(err) != errBundleStopAfterPull {
		// find bundle locally
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		dgst, err := daemon.bundleReferenceStore.Get(ref)
		if err != nil {
			return "", errors.Wrapf(err, "failed to find bundle locally by %v", ref)
		}
		bundle, err := daemon.bundleStore.Get(bundle.IDFromDigest(dgst))
		if err != nil {
			return "", errors.Wrapf(err, "failed to get local bundle by %v", dgst)
		}
		for _, s := range bundle.Services {
			if s.Name == imageName {
				return s.Image, nil
			}
		}
	}
	if len(selector.pulled) != 1 {
		return "", errors.Errorf("invalid number of images pulled: %v", selector.pulled)
	}
	return selector.pulled[0], nil
}

var errBundleStopAfterPull = errors.New("pulling stopped after config")

type bundleImageSelector struct {
	config []byte
	name   string
	pulled []image.ID
}

func (b *bundleImageSelector) Create(config []byte) (bundle.ID, error) {
	b.config = config
	return "", errBundleStopAfterPull
}
func (b *bundleImageSelector) Get(id bundle.ID) (*bundle.Bundle, error) {
	return nil, errors.Errorf("bundle %v not found", id)
}
func (b *bundleImageSelector) Select(name string) bool {
	return b.name != "" && name == b.name
}
func (b *bundleImageSelector) Pulled(id image.ID) {
	b.pulled = append(b.pulled, id)
}
