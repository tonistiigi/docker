package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/content/local"
	"github.com/docker/docker/builder/builder-next/containerimage"
	containerimageexp "github.com/docker/docker/builder/builder-next/exporter"
	"github.com/docker/docker/builder/builder-next/snapshot"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/runcexecutor"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/snapshot/blobmapping"
	"github.com/moby/buildkit/source"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func dummyBuildKitSetup(opts routerOptions) error {

	root := "/var/lib/docker-buildkit"

	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}

	dist := opts.daemon.DistributionServices()

	var driver graphdriver.Driver
	if ls, ok := dist.LayerStore.(interface {
		Driver() graphdriver.Driver
	}); ok {
		driver = ls.Driver()
	} else {
		return errors.Errorf("could not access graphdriver")
	}

	sbase, err := snapshot.NewSnapshotter(snapshot.Opt{
		GraphDriver: driver,
		LayerStore:  dist.LayerStore,
		Root:        root,
	})
	if err != nil {
		return err
	}

	store, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return err
	}

	md, err := metadata.NewStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return err
	}

	snapshotter := blobmapping.NewSnapshotter(blobmapping.Opt{
		Content:       store,
		Snapshotter:   sbase,
		MetadataStore: md,
	})

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
	})
	if err != nil {
		return err
	}

	src, err := containerimage.NewSource(containerimage.SourceOpt{
		SessionManager:  opts.sessionManager,
		CacheAccessor:   cm,
		ContentStore:    store,
		DownloadManager: dist.DownloadManager,
		MetadataStore:   dist.V2MetadataService,
	})
	if err != nil {
		return err
	}

	exec, err := runcexecutor.New(runcexecutor.Opt{
		Root:              filepath.Join(root, "executor"),
		CommandCandidates: []string{"docker-runc", "runc"},
	})
	if err != nil {
		return err
	}

	differ, ok := sbase.(containerimageexp.Differ)
	if !ok {
		return errors.Errorf("snapshotter doesn't support differ")
	}

	exp, err := containerimageexp.New(containerimageexp.Opt{
		ImageStore:     dist.ImageStore,
		ReferenceStore: dist.ReferenceStore,
		Differ:         differ,
	})
	if err != nil {
		return err
	}

	e, err := exp.Resolve(context.TODO(), nil)
	if err != nil {
		return err
	}

	go func() {
		time.Sleep(1 * time.Second)
		if err := demo(src, exec, cm, e); err != nil {
			logrus.Errorf("err: %+v", err)
		}
		logrus.Debugf("demo done")
	}()

	_ = src
	return nil
}

func demo(src source.Source, exec executor.Executor, cm cache.Manager, exp exporter.ExporterInstance) error {
	logrus.Debugf("demo")
	ctx := context.TODO()
	id, err := source.NewImageIdentifier("docker.io/library/alpine:latest")
	if err != nil {
		return err
	}
	is, err := src.Resolve(ctx, id)
	if err != nil {
		return err
	}
	key, err := is.CacheKey(ctx)
	if err != nil {
		return err
	}
	logrus.Debugf("cachekey is: %s", key)

	ref, err := is.Snapshot(ctx)
	if err != nil {
		return err
	}

	ref2, err := runCommand(ctx, cm, exec, ref, "touch /foobar && ls -l")
	if err != nil {
		return err
	}

	ref3, err := runCommand(ctx, cm, exec, ref2, "touch /foobar2 && ls -l")
	if err != nil {
		return err
	}

	ref.Release(context.TODO())
	ref2.Release(context.TODO())

	if err := exp.Export(ctx, ref3, nil); err != nil {
		return err
	}

	ref3.Release(context.TODO())

	_ = ref3

	return nil
}

func runCommand(ctx context.Context, cm cache.Manager, exec executor.Executor, ref cache.ImmutableRef, cmd string) (cache.ImmutableRef, error) {
	logrus.Debugf("ref is: %v", ref.ID())

	mref, err := cm.New(ctx, ref, cache.WithDescription("demo"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if mref != nil {
			mref.Release(context.TODO())
		}
	}()

	if err := exec.Exec(ctx, executor.Meta{
		Args: []string{"/bin/sh", "-c", cmd},
		Cwd:  "/",
	}, mref, nil, nil, os.Stdout, os.Stdout); err != nil {
		return nil, err
	}

	ref2, err := mref.Commit(ctx)
	if err != nil {
		return nil, err
	}
	mref = nil

	logrus.Debugf("ref2 is %s", ref2.ID())
	return ref2, nil
}
