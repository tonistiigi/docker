package dockerfile

import (
	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/remotecontext"
	"github.com/pkg/errors"
)

// imageContexts is a helper for stacking up built image rootfs and reusing
// them as contexts
type imageContexts struct {
	b    *Builder
	list []*imageMount
}

type imageMount struct {
	id      string
	ctx     builder.Context
	release func() error
}

func (ic *imageContexts) new() {
	ic.list = append(ic.list, &imageMount{})
}

func (ic *imageContexts) update(imageID string) {
	ic.list[len(ic.list)-1].id = imageID
}

func (ic *imageContexts) validate(i int) error {
	if i < 0 || i >= len(ic.list)-1 {
		return errors.Errorf("invalid context value %s", i)
	}
	return nil
}

func (ic *imageContexts) context(i int) (builder.Context, error) {
	if err := ic.validate(i); err != nil {
		return nil, err
	}
	im := ic.list[i]
	if im.ctx == nil {
		if im.id == "" {
			return nil, errors.Errorf("could not copy from empty context")
		}
		p, release, err := ic.b.docker.MountImage(im.id)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", im.id)
		}
		ctx, err := remotecontext.NewLazyContext(p)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create lazycontext for %s", p)
		}
		logrus.Debugf("mounted image: %s %s", im.id, p)
		im.release = release
		im.ctx = ctx
	}
	return im.ctx, nil
}

func (ic *imageContexts) unmount() (retErr error) {
	for _, im := range ic.list {
		if im.release != nil {
			if err := im.release(); err != nil {
				logrus.Error(errors.Wrapf(err, "failed to unmount previous build image"))
				retErr = err
			}
		}
	}
	return
}
