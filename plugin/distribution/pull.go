package distribution

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	plugin "github.com/docker/docker/plugin/types"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/docker/distribution/registry/client"
	archive "github.com/docker/docker/pkg/chrootarchive"
)

type PullData interface {
	Config() ([]byte, error)
	Layer() (io.ReadCloser, error)
}

type pullData struct {
	repository distribution.Repository
	manifest   schema2.Manifest
	index      int
}

func (pd *pullData) Config() ([]byte, error) {
	blobs := pd.repository.Blobs(context.Background())
	config, err := blobs.Get(context.Background(), pd.manifest.Config.Digest)
	if err != nil {
		return nil, err
	}
	// validate
	var p plugin.Plugin
	if err := json.Unmarshal(config, &p); err != nil {
		return nil, err
	}
	return config, nil
}

func (pd *pullData) Layer() (io.ReadCloser, error) {
	if pd.index >= len(pd.manifest.Layers) {
		return nil, io.EOF
	}

	blobs := pd.repository.Blobs(context.Background())
	rsc, err := blobs.Open(context.Background(), pd.manifest.Layers[pd.index].Digest)
	if err != nil {
		return nil, err
	}
	pd.index++
	return rsc, nil
}

func Pull(url, name string) (PullData, error) {
	ref, err := reference.ParseNamed(name)
	if err != nil {
		return nil, err
	}

	repository, err := client.NewRepository(context.Background(), ref, url, &http.Transport{})
	if err != nil {
		return nil, err
	}

	tag := DefaultTag
	if tagged, ok := ref.(reference.NamedTagged); ok {
		tag = tagged.Tag()
	}
	// tags := repository.Tags(context.Background())
	// desc, err := tags.Get(context.Background(), tag)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	//
	msv, err := repository.Manifests(context.Background())
	if err != nil {
		return nil, err
	}
	manifest, err := msv.Get(context.Background(), "", client.WithTag(tag))
	if err != nil {
		return nil, err
	}

	_, pl, err := manifest.Payload()
	if err != nil {
		return nil, err
	}
	var m schema2.Manifest
	if err := json.Unmarshal(pl, &m); err != nil {
		return nil, err
	}

	pd := &pullData{
		repository: repository,
		manifest:   m,
	}

	logrus.Debugf("manifest: %s", pl)
	return pd, nil
}

func WritePullData(pd PullData, dest string, extract bool) error {
	config, err := pd.Config()
	if err != nil {
		return err
	}
	var p plugin.Plugin
	if err := json.Unmarshal(config, &p); err != nil {
		return err
	}
	logrus.Debugf("%#v", p)

	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}

	if extract {
		if err := ioutil.WriteFile(filepath.Join(dest, "manifest.json"), config, 0600); err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Join(dest, "rootfs"), 0700); err != nil {
			return err
		}
	}

	for i := 0; ; i++ {
		l, err := pd.Layer()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if !extract {
			f, err := os.Create(filepath.Join(dest, fmt.Sprintf("layer%d.tar", i)))
			if err != nil {
				return err
			}
			io.Copy(f, l)
			l.Close()
			f.Close()
			continue
		}

		if _, err := archive.ApplyLayer(filepath.Join(dest, "rootfs"), l); err != nil {
			return err
		}

	}
	return nil
}
