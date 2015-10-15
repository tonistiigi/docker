package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/image"
	"github.com/docker/docker/images"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/migrate/v1"
	"github.com/docker/docker/pkg/broadcaster"
	"github.com/docker/docker/pkg/progressreader"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/registry"
	"golang.org/x/net/context"
)

type v2Puller struct {
	blobSumLookupService  *metadata.BlobSumLookupService
	blobSumStorageService *metadata.BlobSumStorageService
	endpoint              registry.APIEndpoint
	config                *ImagePullConfig
	sf                    *streamformatter.StreamFormatter
	repoInfo              *registry.RepositoryInfo
	repo                  distribution.Repository
	sessionID             string
}

func (p *v2Puller) Pull(ref reference.Named) (fallback bool, err error) {
	// TODO(tiborvass): was ReceiveTimeout
	p.repo, err = NewV2Repository(p.repoInfo, p.endpoint, p.config.MetaHeaders, p.config.AuthConfig, "pull")
	if err != nil {
		logrus.Debugf("Error getting v2 registry: %v", err)
		return true, err
	}

	p.sessionID = stringid.GenerateRandomID()

	if err := p.pullV2Repository(ref); err != nil {
		if registry.ContinueOnError(err) {
			logrus.Debugf("Error trying v2 registry: %v", err)
			return true, err
		}
		return false, err
	}
	return false, nil
}

func (p *v2Puller) pullV2Repository(ref reference.Named) (err error) {
	var refs []reference.Named
	if _, isTagged := ref.(reference.Tagged); isTagged {
		refs = []reference.Named{ref}
	} else {
		var err error

		manSvc, err := p.repo.Manifests(context.Background())
		if err != nil {
			return err
		}

		tags, err := manSvc.Tags()
		if err != nil {
			return err
		}

		// This probably becomes a lot nicer after the manifest
		// refactor...
		for _, tag := range tags {
			tagRef, err := reference.WithTag(p.repoInfo.LocalName, tag)
			if err != nil {
				return err
			}
			refs = append(refs, tagRef)
		}
	}

	poolKey := "v2:" + ref.String()
	broadcaster, found := p.config.Pool.add("pull", poolKey)
	broadcaster.Add(p.config.OutStream)
	if found {
		// Another pull of the same repository is already taking place; just wait for it to finish
		return broadcaster.Wait()
	}

	// This must use a closure so it captures the value of err when the
	// function returns, not when the 'defer' is evaluated.
	defer func() {
		p.config.Pool.removeWithError("pull", poolKey, err)
	}()

	var layersDownloaded bool
	for _, pullRef := range refs {
		// pulledNew is true if either new layers were downloaded OR if existing images were newly tagged
		// TODO(tiborvass): should we change the name of `layersDownload`? What about message in WriteStatus?
		pulledNew, err := p.pullV2Tag(broadcaster, pullRef)
		if err != nil {
			return err
		}
		layersDownloaded = layersDownloaded || pulledNew
	}

	writeStatus(ref.String(), broadcaster, p.sf, layersDownloaded)

	return nil
}

// downloadInfo is used to pass information from download to extractor
type downloadInfo struct {
	tmpFile       *os.File
	digest        digest.Digest
	layerBlobSums []digest.Digest
	layer         distribution.ReadSeekCloser
	size          int64
	err           chan error
	poolKey       string
	broadcaster   *broadcaster.Buffered
}

type errVerification struct{}

func (errVerification) Error() string { return "verification failed" }

func (p *v2Puller) download(di *downloadInfo) {
	logrus.Debugf("pulling blob %q", di.digest)

	blobs := p.repo.Blobs(context.Background())

	desc, err := blobs.Stat(context.Background(), di.digest)
	if err != nil {
		logrus.Debugf("Error statting layer: %v", err)
		di.err <- err
		return
	}
	di.size = desc.Size

	layerDownload, err := blobs.Open(context.Background(), di.digest)
	if err != nil {
		logrus.Debugf("Error fetching layer: %v", err)
		di.err <- err
		return
	}
	defer layerDownload.Close()

	verifier, err := digest.NewDigestVerifier(di.digest)
	if err != nil {
		di.err <- err
		return
	}

	digestStr := di.digest.String()

	reader := progressreader.New(progressreader.Config{
		In:        ioutil.NopCloser(io.TeeReader(layerDownload, verifier)),
		Out:       di.broadcaster,
		Formatter: p.sf,
		Size:      di.size,
		NewLines:  false,
		ID:        stringid.TruncateID(digestStr),
		Action:    "Downloading",
	})
	io.Copy(di.tmpFile, reader)

	di.broadcaster.Write(p.sf.FormatProgress(stringid.TruncateID(digestStr), "Verifying Checksum", nil))

	if !verifier.Verified() {
		err = fmt.Errorf("filesystem layer verification failed for digest %s", di.digest)
		logrus.Error(err)
		di.err <- err
		return
	}

	di.broadcaster.Write(p.sf.FormatProgress(stringid.TruncateID(digestStr), "Download complete", nil))

	logrus.Debugf("Downloaded %s to tempfile %s", digestStr, di.tmpFile.Name())
	di.layer = layerDownload

	di.err <- nil
}

func (p *v2Puller) pullV2Tag(out io.Writer, ref reference.Named) (tagUpdated bool, err error) {
	tagged, isTagged := ref.(reference.Tagged)
	if !isTagged {
		return false, fmt.Errorf("reference %s has no tag", ref.String())
	}
	tag := tagged.Tag()

	logrus.Debugf("Pulling ref from V2 registry: %q", tag)

	manSvc, err := p.repo.Manifests(context.Background())
	if err != nil {
		return false, err
	}

	unverifiedManifest, err := manSvc.GetByTag(tag)
	if err != nil {
		return false, err
	}
	if unverifiedManifest == nil {
		return false, fmt.Errorf("image manifest does not exist for tag %q", tag)
	}
	var verifiedManifest *manifest.Manifest
	verifiedManifest, err = verifyManifest(unverifiedManifest, ref)
	if err != nil {
		return false, err
	}

	// remove duplicate layers and check parent chain validity
	err = fixManifestLayers(verifiedManifest)
	if err != nil {
		return false, err
	}

	out.Write(p.sf.FormatStatus(tag, "Pulling from %s", p.repo.Name()))

	var downloads []*downloadInfo

	var referencedLayers []layer.Layer
	defer func() {
		for _, l := range referencedLayers {
			p.config.LayerStore.Release(l)
		}

		for _, d := range downloads {
			p.config.Pool.removeWithError("pull", d.poolKey, err)
			if d.tmpFile != nil {
				d.tmpFile.Close()
				if err := os.RemoveAll(d.tmpFile.Name()); err != nil {
					logrus.Errorf("Failed to remove temp file: %s", d.tmpFile.Name())
				}
			}
		}
	}()

	var allBlobSums []digest.Digest

	// Generate a slice of BlobSums, ordered from bottom-most to top-most
	for i := len(verifiedManifest.FSLayers); i >= 0; i-- {
		allBlobSums = append(allBlobSums, verifiedManifest.FSLayers[i].BlobSum)
	}

	var layerID layer.ID
	var layerDiffIDs []layer.DiffID

	// Iterating from top-most to bottom-most layer
	var i int
	for i = len(allBlobSums); i > 0; i-- {
		// Do we have a layer on disk corresponding to the set of
		// blobsums up to this point?
		layerID, err = p.blobSumLookupService.Get(allBlobSums[:i])
		if err != nil {
			continue
		}

		if l, err := p.config.LayerStore.Get(layerID); err == nil {
			referencedLayers = append(referencedLayers, l)
			layerDiffIDs = addLayerDiffIDs(l)
			break
		}
	}

	// Everything that wasn't found on disk needs to be downloaded.
	// Note that the order of this loop is in the direction of bottom-most
	// to top-most, so that the downloads slice gets ordered correctly.
	for ; i < len(allBlobSums); i++ {
		blobSum := allBlobSums[i]

		out.Write(p.sf.FormatProgress(stringid.TruncateID(blobSum.String()), "Pulling fs layer", nil))

		d := &downloadInfo{
			poolKey:       "v2layer:" + blobSum.String(),
			digest:        blobSum,
			layerBlobSums: allBlobSums[:i+1],
			// TODO: seems like this chan buffer solved hanging problem in go1.5,
			// this can indicate some deeper problem that somehow we never take
			// error from channel in loop below
			err: make(chan error, 1),
		}

		tmpFile, err := ioutil.TempFile("", "GetImageBlob")
		if err != nil {
			return false, err
		}
		d.tmpFile = tmpFile

		downloads = append(downloads, d)

		broadcaster, found := p.config.Pool.add("pull", d.poolKey)
		broadcaster.Add(out)
		d.broadcaster = broadcaster
		if found {
			d.err <- nil
		} else {
			go p.download(d)
		}
	}

	for _, d := range downloads {
		if err := <-d.err; err != nil {
			return false, err
		}

		if d.layer == nil {
			// Wait for a different pull to download and extract
			// this layer.
			err = d.broadcaster.Wait()
			if err != nil {
				return false, err
			}
			continue
		}

		d.tmpFile.Seek(0, 0)
		reader := progressreader.New(progressreader.Config{
			In:        d.tmpFile,
			Out:       d.broadcaster,
			Formatter: p.sf,
			Size:      d.size,
			NewLines:  false,
			ID:        stringid.TruncateID(d.digest.String()),
			Action:    "Extracting",
		})

		l, err := p.config.LayerStore.Register(reader, layerID)
		if err != nil {
			return false, err
		}
		referencedLayers = append(referencedLayers, l)

		layerID = l.ID()
		layerDiffIDs = append(layerDiffIDs, l.DiffID())

		// Cache mapping from the set of blobsums so far to this layer ID
		if err := p.blobSumLookupService.Set(d.layerBlobSums, layerID); err != nil {
			return false, err
		}
		// Cache mapping from this layer's DiffID to the blobsum
		if err := p.blobSumStorageService.Add(l.DiffID(), d.digest); err != nil {
			return false, err
		}

		d.broadcaster.Write(p.sf.FormatProgress(stringid.TruncateID(d.digest.String()), "Pull complete", nil))
		d.broadcaster.Close()
		tagUpdated = true
	}

	// Create a new-style config from the legacy config in the manifest
	var history []images.History
	for i := len(verifiedManifest.History) - 1; i >= 0; i-- {
		h, err := v1.HistoryFromV1Config([]byte(verifiedManifest.History[i].V1Compatibility))
		if err != nil {
			return false, err
		}
		history = append(history, h)
	}
	config, err := v1.ConfigFromV1Config([]byte(verifiedManifest.History[0].V1Compatibility), layerDiffIDs, history)
	if err != nil {
		return false, err
	}

	imageID, err := p.config.ImageStore.Create(config)
	if err != nil {
		return false, err
	}

	manifestDigest, _, err := digestFromManifest(unverifiedManifest, p.repoInfo.LocalName.Name())
	if err != nil {
		return false, err
	}

	// Check for new tag if no layers downloaded
	var oldTagImageID images.ID
	if !tagUpdated {
		oldTagImageID, err = p.config.TagStore.Get(ref)
		if err != nil || oldTagImageID != imageID {
			tagUpdated = true
		}
	}

	if tagUpdated {
		if err = p.config.TagStore.Add(ref, imageID, true); err != nil {
			return false, err
		}
	}

	if manifestDigest != "" {
		out.Write(p.sf.FormatStatus("", "Digest: %s", manifestDigest))
	}

	return tagUpdated, nil
}

// addLayerDiffIDs creates a slice containing all layer diff IDs for the given
// layer, ordered from base to top-most.
func addLayerDiffIDs(l layer.Layer) []layer.DiffID {
	parent, err := l.Parent()
	if err != nil || parent == nil {
		return []layer.DiffID{l.DiffID()}
	}
	return append(addLayerDiffIDs(parent), l.DiffID())
}

func verifyManifest(signedManifest *manifest.SignedManifest, ref reference.Reference) (m *manifest.Manifest, err error) {
	// If pull by digest, then verify the manifest digest. NOTE: It is
	// important to do this first, before any other content validation. If the
	// digest cannot be verified, don't even bother with those other things.
	if digested, isDigested := ref.(reference.Digested); isDigested {
		verifier, err := digest.NewDigestVerifier(digested.Digest())
		if err != nil {
			return nil, err
		}
		payload, err := signedManifest.Payload()
		if err != nil {
			// If this failed, the signatures section was corrupted
			// or missing. Treat the entire manifest as the payload.
			payload = signedManifest.Raw
		}
		if _, err := verifier.Write(payload); err != nil {
			return nil, err
		}
		if !verifier.Verified() {
			err := fmt.Errorf("image verification failed for digest %s", digested.Digest())
			logrus.Error(err)
			return nil, err
		}

		var verifiedManifest manifest.Manifest
		if err = json.Unmarshal(payload, &verifiedManifest); err != nil {
			return nil, err
		}
		m = &verifiedManifest
	} else {
		m = &signedManifest.Manifest
	}

	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schema version %d for %q", m.SchemaVersion, ref.String())
	}
	if len(m.FSLayers) != len(m.History) {
		return nil, fmt.Errorf("length of history not equal to number of layers for %q", ref.String())
	}
	if len(m.FSLayers) == 0 {
		return nil, fmt.Errorf("no FSLayers in manifest for %q", ref.String())
	}
	return m, nil
}

// fixManifestLayers removes repeated layers from the manifest and checks the
// correctness of the parent chain.
func fixManifestLayers(m *manifest.Manifest) error {
	images := make([]*image.Image, len(m.FSLayers))
	for i := range m.FSLayers {
		img, err := image.NewImgJSON([]byte(m.History[i].V1Compatibility))
		if err != nil {
			return err
		}
		images[i] = img
		if err := image.ValidateID(img.ID); err != nil {
			return err
		}
	}

	if images[len(images)-1].Parent != "" {
		return errors.New("Invalid parent ID in the base layer of the image.")
	}

	// check general duplicates to error instead of a deadlock
	idmap := make(map[string]struct{})

	var lastID string
	for _, img := range images {
		// skip IDs that appear after each other, we handle those later
		if _, exists := idmap[img.ID]; img.ID != lastID && exists {
			return fmt.Errorf("ID %+v appears multiple times in manifest", img.ID)
		}
		lastID = img.ID
		idmap[lastID] = struct{}{}
	}

	// backwards loop so that we keep the remaining indexes after removing items
	for i := len(images) - 2; i >= 0; i-- {
		if images[i].ID == images[i+1].ID { // repeated ID. remove and continue
			m.FSLayers = append(m.FSLayers[:i], m.FSLayers[i+1:]...)
			m.History = append(m.History[:i], m.History[i+1:]...)
		} else if images[i].Parent != images[i+1].ID {
			return fmt.Errorf("Invalid parent ID. Expected %v, got %v.", images[i+1].ID, images[i].Parent)
		}
	}

	return nil
}
