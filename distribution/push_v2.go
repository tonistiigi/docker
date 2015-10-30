package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/migrate/v1"
	"github.com/docker/docker/pkg/progressreader"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/registry"
	"github.com/docker/docker/tag"
	"golang.org/x/net/context"
)

type v2Pusher struct {
	blobSumLookupService  *metadata.BlobSumLookupService
	blobSumStorageService *metadata.BlobSumStorageService
	ref                   reference.Named
	endpoint              registry.APIEndpoint
	repoInfo              *registry.RepositoryInfo
	config                *ImagePushConfig
	sf                    *streamformatter.StreamFormatter
	repo                  distribution.Repository

	// layersPushed is the set of layers known to exist on the remote side.
	// This avoids redundant queries when pushing multiple tags that
	// involve the same layers.
	layersPushed map[digest.Digest]bool
}

func (p *v2Pusher) Push() (fallback bool, err error) {
	p.repo, err = NewV2Repository(p.repoInfo, p.endpoint, p.config.MetaHeaders, p.config.AuthConfig, "push", "pull")
	if err != nil {
		logrus.Debugf("Error getting v2 registry: %v", err)
		return true, err
	}

	localName := p.repoInfo.LocalName.Name()

	var associations []tag.Association
	if _, isTagged := p.ref.(reference.Tagged); isTagged {
		imageID, err := p.config.TagStore.Get(p.ref)
		if err != nil {
			return false, fmt.Errorf("tag does not exist: %s", p.ref.String())
		}

		associations = []tag.Association{
			tag.Association{
				Ref:     p.ref,
				ImageID: imageID,
			},
		}
	} else {
		// Pull all tags
		associations = p.config.TagStore.ReferencesByName(p.ref)
	}
	if err != nil {
		return false, fmt.Errorf("error getting tags for %s: %s", localName, err)
	}
	if len(associations) == 0 {
		return false, fmt.Errorf("no tags to push for %s", localName)
	}

	for _, association := range associations {
		if err := p.pushV2Tag(association); err != nil {
			return false, err
		}
	}

	return false, nil
}

func (p *v2Pusher) pushV2Tag(association tag.Association) error {
	ref := association.Ref
	logrus.Debugf("Pushing repository: %s", ref.String())

	img, err := p.config.ImageStore.Get(association.ImageID)
	if err != nil {
		return fmt.Errorf("could not find image from tag %s: %v", ref.String(), err)
	}

	out := p.config.OutStream

	var fsLayers []manifest.FSLayer
	var l layer.Layer

	topLayerID := img.GetTopLayerID()
	if topLayerID == "" {
		l = layer.EmptyLayer
	} else {
		l, err = p.config.LayerStore.Get(topLayerID)
		if err != nil {
			return fmt.Errorf("failed to get top layer from image: %v", err)
		}
		defer p.config.LayerStore.Release(l)
	}

	// Blobsums of layers involved in this push operation (including those
	// which already exist on the registery), ordered from bottom-most to
	// top-most.
	var layerBlobsums []digest.Digest
	// Layer objects, ordered the same way.
	var layers []layer.Layer

	for l != nil {
		logrus.Debugf("Pushing layer: %s", l.DiffID())

		// Do we have any blobsums associated with this layer's DiffID?
		var exists bool
		var dgst digest.Digest
		possibleBlobsums, err := p.blobSumStorageService.Get(l.DiffID())
		if err == nil {
			dgst, exists, err = p.blobSumAlreadyExists(possibleBlobsums)
			if err != nil {
				out.Write(p.sf.FormatProgress(stringid.TruncateID(string(l.DiffID())), "Image push failed", nil))
				return err
			}
			if exists {
				out.Write(p.sf.FormatProgress(stringid.TruncateID(string(l.DiffID())), "Layer already exists", nil))
			}
		}

		// if digest was empty or not saved, or if blob does not exist on the remote repository,
		// then push the blob.
		if !exists {
			pushDigest, err := p.pushV2Layer(p.repo.Blobs(context.Background()), l)
			if err != nil {
				return err
			}
			// Cache mapping from this layer's DiffID to the blobsum
			if err := p.blobSumStorageService.Add(l.DiffID(), pushDigest); err != nil {
				return err
			}

			dgst = pushDigest
		}

		layers = append([]layer.Layer{l}, layers...)
		layerBlobsums = append([]digest.Digest{dgst}, layerBlobsums...)

		fsLayers = append(fsLayers, manifest.FSLayer{BlobSum: dgst})

		p.layersPushed[dgst] = true

		l = l.Parent()
	}

	// Cache mapping from each layer's set of blobsums to that layer's ID
	for i, l := range layers {
		if err := p.blobSumLookupService.Set(layerBlobsums[:i+1], l.ID()); err != nil {
			return err
		}
	}

	var tag string
	if tagged, isTagged := ref.(reference.Tagged); isTagged {
		tag = tagged.Tag()
	}
	m, err := CreateV2Manifest(p.repo.Name(), tag, img, fsLayers)
	if err != nil {
		return err
	}

	logrus.Infof("Signed manifest for %s using daemon's key: %s", ref.String(), p.config.TrustKey.KeyID())
	signed, err := manifest.Sign(m, p.config.TrustKey)
	if err != nil {
		return err
	}

	manifestDigest, manifestSize, err := digestFromManifest(signed, p.repo.Name())
	if err != nil {
		return err
	}
	if manifestDigest != "" {
		if tagged, isTagged := ref.(reference.Tagged); isTagged {
			// NOTE: do not change this format without first changing the trust client
			// code. This information is used to determine what was pushed and should be signed.
			out.Write(p.sf.FormatStatus("", "%s: digest: %s size: %d", tagged.Tag(), manifestDigest, manifestSize))
		}
	}

	manSvc, err := p.repo.Manifests(context.Background())
	if err != nil {
		return err
	}
	return manSvc.Put(signed)
}

// blobSumAlreadyExists checks if the registry already know about any of the
// blobsums passed in the "blobsums" slice. If it finds one that the registry
// knows about, it returns the known digest and "true".
func (p *v2Pusher) blobSumAlreadyExists(blobsums []digest.Digest) (digest.Digest, bool, error) {
	for _, dgst := range blobsums {
		if p.layersPushed[dgst] {
			// it is already known that the push is not needed and
			// therefore doing a stat is unnecessary
			return dgst, true, nil
		}
		_, err := p.repo.Blobs(context.Background()).Stat(context.Background(), dgst)
		switch err {
		case nil:
			return dgst, true, nil
		case distribution.ErrBlobUnknown:
			// nop
		default:
			return "", false, err
		}
	}
	return "", false, nil
}

// CreateV2Manifest creates a V2 manifest from an image config and set of
// FSLayer digests.
// FIXME: This should be moved to the distribution repo, since it will also
// be useful for converting new manifests to the old format.
func CreateV2Manifest(name, tag string, img *image.Image, fsLayers []manifest.FSLayer) (*manifest.Manifest, error) {
	if len(fsLayers) == 0 {
		return nil, errors.New("empty fsLayers list when trying to create V2 manifest")
	}

	// Generate IDs for each layer
	v1IDsLen := len(fsLayers)
	if v1IDsLen < 2 {
		v1IDsLen = 2
	}
	v1IDs := make([]string, v1IDsLen)
	parent := ""
	for i := len(fsLayers) - 1; i > 0; i-- {
		dgst, err := digest.FromBytes([]byte(fsLayers[i].BlobSum.Hex() + " " + parent))
		if err != nil {
			return nil, err
		}
		parent = dgst.Hex()
		v1IDs[i] = parent
	}

	dgst, err := digest.FromBytes([]byte(fsLayers[0].BlobSum.Hex() + " " + parent + " " + string(img.RawJSON())))
	if err != nil {
		return nil, err
	}
	v1IDs[0] = dgst.Hex()

	history := make([]manifest.History, len(fsLayers))

	// Top-level v1compatibility string should be a modified version of the
	// image config.
	transformedConfig, err := v1.V1ConfigFromConfig(img, v1IDs[0], v1IDs[1])
	if err != nil {
		return nil, err
	}

	history[0].V1Compatibility = string(transformedConfig)

	// For the rest of the history, create fake V1Compatibility strings
	// that fit the format and don't collide with anything else, but don't
	// result in runnable images on their own.
	for i := 1; i < len(fsLayers); i++ {
		// FIXME: are there other fields that are mandatory to the pull code?
		if i != len(fsLayers)-1 {
			history[i].V1Compatibility = fmt.Sprintf(`{"id":"%s","parent":"%s"}`, v1IDs[i], v1IDs[i+1])
		} else {
			history[i].V1Compatibility = fmt.Sprintf(`{"id":"%s"}`, v1IDs[i])
		}
	}

	return &manifest.Manifest{
		Versioned: manifest.Versioned{
			SchemaVersion: 1,
		},
		Name:         name,
		Tag:          tag,
		Architecture: img.Architecture,
		FSLayers:     fsLayers,
		History:      history,
	}, nil
}

func rawJSON(value interface{}) *json.RawMessage {
	jsonval, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return (*json.RawMessage)(&jsonval)
}

func (p *v2Pusher) pushV2Layer(bs distribution.BlobService, l layer.Layer) (digest.Digest, error) {
	out := p.config.OutStream
	displayID := stringid.TruncateID(string(l.DiffID()))

	out.Write(p.sf.FormatProgress(displayID, "Preparing", nil))

	arch, err := l.TarStream()
	if err != nil {
		return "", err
	}

	// Send the layer
	layerUpload, err := bs.Create(context.Background())
	if err != nil {
		return "", err
	}
	defer layerUpload.Close()

	// don't care if this fails; best effort
	size, _ := l.Size()

	reader := progressreader.New(progressreader.Config{
		In:        ioutil.NopCloser(arch), // we'll take care of close here.
		Out:       out,
		Formatter: p.sf,
		Size:      size,
		NewLines:  false,
		ID:        displayID,
		Action:    "Pushing",
	})

	compressedReader := compress(reader)

	digester := digest.Canonical.New()
	tee := io.TeeReader(compressedReader, digester.Hash())

	out.Write(p.sf.FormatProgress(displayID, "Pushing", nil))
	nn, err := layerUpload.ReadFrom(tee)
	compressedReader.Close()
	if err != nil {
		return "", err
	}

	dgst := digester.Digest()
	if _, err := layerUpload.Commit(context.Background(), distribution.Descriptor{Digest: dgst}); err != nil {
		return "", err
	}

	logrus.Debugf("uploaded layer %s (%s), %d bytes", l.DiffID(), dgst, nn)
	out.Write(p.sf.FormatProgress(displayID, "Pushed", nil))

	return dgst, nil
}
