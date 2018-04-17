package builder

import (
	"context"
	"path/filepath"
	"time"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/content/local"
	"github.com/docker/docker/builder/builder-next/worker"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver-next/boltdbcachestorage"
	"github.com/moby/buildkit/util/throttle"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Builder defines interface for running a build
type Builder interface {
	Build(context.Context, backend.BuildConfig) (*builder.Result, error)
}

// Result is the output produced by a Builder
type Result struct {
	ImageID string
	// FromImage Image
}

type Opt struct {
	SessionManager *session.Manager
	Root           string
}

func New(opt Opt) (builderbase.Builder, error) {

	cacheStorage, err := boltdbcachestorage.NewStore(filepath.Join(opt.Root, "cache.db"))
	if err != nil {
		return nil, err
	}

	frontends := map[string]frontend.Frontend{}
	frontends["dockerfile.v0"] = dockerfile.NewDockerfileFrontend()
	// frontends["gateway.v0"] = gateway.NewGatewayFrontend()

	// type WorkerOpt struct {
	// 	ID             string
	// 	Labels         map[string]string
	// 	SessionManager *session.Manager
	// 	MetadataStore  *metadata.Store
	// 	Executor       executor.Executor
	// 	Snapshotter    snapshot.Snapshotter
	// 	ContentStore   content.Store
	// 	Applier        diff.Applier
	// 	Differ         diff.Comparer
	// 	ImageStore     images.Store // optional
	// }

	md, err := metadata.NewStore(filepath.Join(opt.Root, "metadata.db"))
	if err != nil {
		return nil, err
	}

	s, err := snFactory.New(filepath.Join(root, "snapshots"))
	if err != nil {
		return opt, err
	}

	c, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return opt, err
	}

	db, err := bolt.Open(filepath.Join(root, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return opt, err
	}

	mdb := ctdmetadata.NewDB(db, c, map[string]ctdsnapshot.Snapshotter{
		"moby": s,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return opt, err
	}

	throttledGC := throttle.Throttle(time.Second, func() {
		if _, err := mdb.GarbageCollect(context.TODO()); err != nil {
			logrus.Errorf("GC error: %+v", err)
		}
	})

	gc := func(ctx context.Context) error {
		throttledGC()
		return nil
	}

	wopt := WorkerOpt{
		ID:             "moby",
		SessionManager: opt.SessionManager,
		MetadataStore:  md,
		ContentStore:   c,
		Snapshotter:    containerdsnapshot.NewSnapshotter(mdb.Snapshotter("moby"), c, md, "buildkit", gc),
	}

	wc := &worker.Controller{}
	w, err := worker.NewWorker(opt)
	if err != nil {
		return nil, err
	}
	wc.Add(w)

	// return control.NewController(control.Opt{
	// 	SessionManager:   opt.SessionManager,
	// 	WorkerController: wc,
	// 	Frontends:        frontends,
	// 	CacheKeyStorage:  cacheStorage,
	// })
	return nil, errors.Errorf("not implemented")
}
