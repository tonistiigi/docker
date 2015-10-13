package distribution

import (
	"fmt"
	"io"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/cliconfig"
	"github.com/docker/docker/daemon/events"
	"github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/registry"
	"github.com/docker/docker/tag"
)

// ImagePullConfig stores pull configuration.
type ImagePullConfig struct {
	// MetaHeaders stores HTTP headers with metadata about the image
	// (DockerHeaders with prefix X-Meta- in the request).
	MetaHeaders map[string][]string
	// AuthConfig holds authentication credentials for authenticating with
	// the registry.
	AuthConfig *cliconfig.AuthConfig
	// OutStream is the output writer for showing the status of the pull
	// operation.
	OutStream io.Writer
	// RegistryService is the registry service to use for TLS configuration
	// and endpoint lookup.
	RegistryService *registry.Service
	// EventsService is the events service to use for logging.
	EventsService *events.Events
	// MetadataStore is the storage backend for distribution-specific
	// metadata.
	MetadataStore metadata.Store
	// LayerStore manages layers.
	LayerStore layer.Store
	// ImageStore manages images.
	ImageStore images.Store
	// TagStore manages tags.
	TagStore tag.Store
	// Pool manages concurrent pulls and pushes.
	Pool *Pool
}

// Puller is an interface that abstracts pulling for different API versions.
type Puller interface {
	// Pull tries to pull the image referenced by `tag`
	// Pull returns an error if any, as well as a boolean that determines whether to retry Pull on the next configured endpoint.
	//
	Pull(ref reference.Named) (fallback bool, err error)
}

// NewPuller returns a Puller interface that will pull from either a v1 or v2
// registry. The endpoint argument contains a Version field that determines
// whether a v1 or v2 puller will be created. The other parameters are passed
// through to the underlying puller implementation for use during the actual
// pull operation.
func NewPuller(endpoint registry.APIEndpoint, repoInfo *registry.RepositoryInfo, imagePullConfig *ImagePullConfig, sf *streamformatter.StreamFormatter) (Puller, error) {
	switch endpoint.Version {
	case registry.APIVersion2:
		return &v2Puller{
			blobSumLookupService:  metadata.NewBlobSumLookupService(imagePullConfig.MetadataStore),
			blobSumStorageService: metadata.NewBlobSumStorageService(imagePullConfig.MetadataStore),
			endpoint:              endpoint,
			config:                imagePullConfig,
			sf:                    sf,
			repoInfo:              repoInfo,
		}, nil
	case registry.APIVersion1:
		panic("v1 unsupported")
		// FIXME
		/*return &v1Puller{
			v1IDService: metadata.NewV1IDService(metadataStore),
			endpoint:    endpoint,
			config:      imagePullConfig,
			sf:          sf,
			repoInfo:    repoInfo,
		}, nil*/
	}
	return nil, fmt.Errorf("unknown version %d for registry %s", endpoint.Version, endpoint.URL)
}

// Pull initiates a pull operation. image is the repository name to pull, and
// tag may be either empty, or indicate a specific tag to pull.
func Pull(ref reference.Named, imagePullConfig *ImagePullConfig) error {
	var sf = streamformatter.NewJSONStreamFormatter()

	// Resolve the Repository name from fqn to RepositoryInfo
	repoInfo, err := imagePullConfig.RegistryService.ResolveRepository(ref)
	if err != nil {
		return err
	}

	// makes sure name is not empty or `scratch`
	if err := validateRepoName(repoInfo.LocalName.Name()); err != nil {
		return err
	}

	endpoints, err := imagePullConfig.RegistryService.LookupPullEndpoints(repoInfo.CanonicalName)
	if err != nil {
		return err
	}

	logName := repoInfo.LocalName
	if tagged, isTagged := ref.(reference.Tagged); isTagged {
		if logName, err = reference.WithTag(repoInfo.LocalName, tagged.Tag()); err != nil {
			return err
		}
	}

	var (
		lastErr error

		// discardNoSupportErrors is used to track whether an endpoint encountered an error of type registry.ErrNoSupport
		// By default it is false, which means that if a ErrNoSupport error is encountered, it will be saved in lastErr.
		// As soon as another kind of error is encountered, discardNoSupportErrors is set to true, avoiding the saving of
		// any subsequent ErrNoSupport errors in lastErr.
		// It's needed for pull-by-digest on v1 endpoints: if there are only v1 endpoints configured, the error should be
		// returned and displayed, but if there was a v2 endpoint which supports pull-by-digest, then the last relevant
		// error is the ones from v2 endpoints not v1.
		discardNoSupportErrors bool
	)
	for _, endpoint := range endpoints {
		logrus.Debugf("Trying to pull %s from %s %s", repoInfo.LocalName, endpoint.URL, endpoint.Version)

		puller, err := NewPuller(endpoint, repoInfo, imagePullConfig, sf)
		if err != nil {
			lastErr = err
			continue
		}
		if fallback, err := puller.Pull(ref); err != nil {
			if fallback {
				if _, ok := err.(registry.ErrNoSupport); !ok {
					// Because we found an error that's not ErrNoSupport, discard all subsequent ErrNoSupport errors.
					discardNoSupportErrors = true
					// save the current error
					lastErr = err
				} else if !discardNoSupportErrors {
					// Save the ErrNoSupport error, because it's either the first error or all encountered errors
					// were also ErrNoSupport errors.
					lastErr = err
				}
				continue
			}
			logrus.Debugf("Not continuing with error: %v", err)
			return err

		}

		imagePullConfig.EventsService.Log("pull", logName.String(), "")
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no endpoints found for %s", ref.Name())
	}
	return lastErr
}

// writeStatus writes a status message to out. If layersDownloaded is true, the
// status message indicates that a newer image was downloaded. Otherwise, it
// indicates that the image is up to date. requestedTag is the tag the message
// will refer to.
func writeStatus(requestedTag string, out io.Writer, sf *streamformatter.StreamFormatter, layersDownloaded bool) {
	if layersDownloaded {
		out.Write(sf.FormatStatus("", "Status: Downloaded newer image for %s", requestedTag))
	} else {
		out.Write(sf.FormatStatus("", "Status: Image is up to date for %s", requestedTag))
	}
}

// validateRepoName validates the name of a repository.
func validateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("Repository name can't be empty")
	}
	if name == "scratch" {
		return fmt.Errorf("'scratch' is a reserved name")
	}
	return nil
}
