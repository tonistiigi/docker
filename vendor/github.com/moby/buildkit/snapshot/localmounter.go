package snapshot

import (
	"io/ioutil"
	"os"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/pkg/errors"
)

type Mounter interface {
	Mount() (string, error)
	Unmount() error
}

// LocalMounter is a helper for mounting mountfactory to temporary path. In
// addition it can mount binds without privileges
func LocalMounter(mf MountFactory) Mounter {
	return &localMounter{mf: mf}
}

// LocalMounterWithMounts is a helper for mounting to temporary path. In
// addition it can mount binds without privileges
func LocalMounterWithMounts(m []mount.Mount) Mounter {
	return &localMounter{m: m}
}

type localMounter struct {
	mu      sync.Mutex
	m       []mount.Mount
	mf      MountFactory
	target  string
	release func() error
}

func (lm *localMounter) Mount() (string, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if lm.m == nil {
		mounts, release, err := lm.mf.Mount()
		if err != nil {
			return "", err
		}
		lm.m = mounts
		lm.release = release
	}

	if len(lm.m) == 1 && (lm.m[0].Type == "bind" || lm.m[0].Type == "rbind") {
		ro := false
		for _, opt := range lm.m[0].Options {
			if opt == "ro" {
				ro = true
				break
			}
		}
		if !ro {
			return lm.m[0].Source, nil
		}
	}

	dir, err := ioutil.TempDir("", "buildkit-mount")
	if err != nil {
		return "", errors.Wrap(err, "failed to create temp dir")
	}

	if err := mount.All(lm.m, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	lm.target = dir
	return dir, nil
}
