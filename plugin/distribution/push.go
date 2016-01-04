package distribution

import (
	"crypto/sha256"
	"io"
	"net/http"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/docker/distribution/registry/client"
)

func Push(url, name string, config io.ReadCloser, layers []io.ReadCloser) (digest.Digest, error) {
	ref, err := reference.ParseNamed(name)
	if err != nil {
		return "", err
	}

	repository, err := client.NewRepository(context.Background(), ref, url, &http.Transport{})
	if err != nil {
		return "", err
	}

	blobs := repository.Blobs(context.Background())

	var descs []distribution.Descriptor

	for i, f := range append([]io.ReadCloser{config}, layers...) {
		bw, err := blobs.Create(context.Background())
		if err != nil {
			return "", err
		}
		h := sha256.New()
		r := io.TeeReader(f, h)
		_, err = io.Copy(bw, r)
		if err != nil {
			return "", err
		}
		f.Close()
		mt := MediaTypeLayer
		if i == 0 {
			mt = MediaTypeConfig
		}
		desc, err := bw.Commit(context.Background(), distribution.Descriptor{
			MediaType: mt,
			Digest:    digest.NewDigest("sha256", h),
		})
		if err != nil {
			return "", err
		}
		desc.MediaType = mt
		logrus.Debugf("pushed blob: %s %s", desc.MediaType, desc.Digest)
		descs = append(descs, desc)
	}

	m, err := schema2.FromStruct(schema2.Manifest{Versioned: schema2.SchemaVersion, Config: descs[0], Layers: descs[1:]})
	if err != nil {
		return "", err
	}
	msv, err := repository.Manifests(context.Background())
	if err != nil {
		return "", err
	}

	_, pl, err := m.Payload()
	if err != nil {
		return "", err
	}

	logrus.Debugf("Pushed manifest: %s", pl)

	tag := DefaultTag
	if tagged, ok := ref.(reference.NamedTagged); ok {
		tag = tagged.Tag()
	}

	return msv.Put(context.Background(), m, client.WithTag(tag))
}
