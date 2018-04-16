package snapshot

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/layer"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

var keyParent = []byte("parent")
var keyCommitted = []byte("committed")
var keyChainID = []byte("chainid")
var keySize = []byte("size")

type Opt struct {
	GraphDriver graphdriver.Driver
	LayerStore  layer.Store
	// MetadataStore metadata.Store
	Root string
}

type snapshotter struct {
	opt Opt

	refs map[layer.ChainID]layer.Layer
	db   *bolt.DB
	mu   sync.Mutex
}

var _ snapshot.SnapshotterBase = &snapshotter{}

func NewSnapshotter(opt Opt) (snapshot.SnapshotterBase, error) {
	dbPath := filepath.Join(opt.Root, "snapshots.db")
	db, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open database file %s", dbPath)
	}

	s := &snapshotter{
		opt:  opt,
		db:   db,
		refs: map[layer.ChainID]layer.Layer{},
	}
	return s, nil
}

type MountFactory interface {
	Mount() ([]mount.Mount, func() error, error)
}

type info struct {
	Kind    snapshots.Kind
	Parent  string
	ChainID digest.Digest
	// Blob    digest.Digest
}

type Info struct {
	Kind    snapshots.Kind    // active or committed snapshot
	Name    string            // name or key of snapshot
	Parent  string            `json:",omitempty"` // name of parent snapshot
	Labels  map[string]string `json:",omitempty"` // Labels for snapshot
	Created time.Time         `json:",omitempty"` // Created time
	Updated time.Time         `json:",omitempty"` // Last update time
}

func (s *snapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) error {
	if err := s.opt.GraphDriver.Create(key, parent, nil); err != nil {
		return err
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(key))
		if err != nil {
			return err
		}

		if parent != "" {
			parent, _ = s.getGraphDriverID(parent)
		}

		if err := b.Put(keyParent, []byte(parent)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *snapshotter) chainID(key string) (layer.ChainID, bool) {
	if strings.HasPrefix(key, "sha256:") {
		dgst, err := digest.Parse(key)
		if err != nil {
			return "", false
		}
		return layer.ChainID(dgst), true
	}
	return "", false
}

func (s *snapshotter) getLayer(key string) (layer.Layer, error) {
	id, ok := s.chainID(key)
	if !ok {
		return nil, nil
	}
	s.mu.Lock()
	l, ok := s.refs[id]
	if !ok {
		var err error
		l, err = s.opt.LayerStore.Get(id)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.refs[id] = l
		if err := s.db.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte(id))
			return err
		}); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.mu.Unlock()
	return l, nil
}

func (s *snapshotter) getGraphDriverID(key string) (string, bool) {
	var gdID string
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(key))
		if b == nil {
			return errors.Errorf("not found") // TODO: typed
		}
		v := b.Get(keyCommitted)
		if v != nil {
			gdID = string(v)
		}
		return nil
	}); err != nil || gdID == "" {
		return key, false
	}
	return gdID, true
}

func (s *snapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	if l, err := s.getLayer(key); err != nil {
		return snapshots.Info{}, err
	} else if l != nil {
		var parentID string
		if p := l.Parent(); p != nil {
			parentID = p.ChainID().String()
		}
		info := snapshots.Info{
			Kind:   snapshots.KindCommitted,
			Name:   key,
			Parent: parentID,
		}
		return info, nil
	}

	inf := snapshots.Info{
		Kind: snapshots.KindActive,
	}

	id, committed := s.getGraphDriverID(key)
	if committed {
		inf.Kind = snapshots.KindCommitted
	}

	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(id))
		if b == nil {
			return errors.Errorf("not found") // TODO: typed
		}
		inf.Name = string(key)
		v := b.Get(keyParent)
		if v != nil {
			inf.Parent = string(v)
		}
		return nil
	}); err != nil {
		return snapshots.Info{}, err
	}
	return inf, nil
}

func (s *snapshotter) Mounts(ctx context.Context, key string) (snapshot.MountFactory, error) {
	l, err := s.getLayer(key)
	if err != nil {
		return nil, err
	}
	if l != nil {
		id := identity.NewID()
		rwlayer, err := s.opt.LayerStore.CreateRWLayer(id, l.ChainID(), nil)
		if err != nil {
			return nil, err
		}
		rootfs, err := rwlayer.Mount("")
		if err != nil {
			return nil, err
		}
		mnt := []mount.Mount{{
			Source:  rootfs.Path(),
			Type:    "bind",
			Options: []string{"rbind"},
		}}
		return &constMountFactory{
			mounts: mnt,
			release: func() error {
				_, err := s.opt.LayerStore.ReleaseRWLayer(rwlayer)
				return err
			},
		}, nil
	}

	id, _ := s.getGraphDriverID(key)

	rootfs, err := s.opt.GraphDriver.Get(id, "")
	if err != nil {
		return nil, err
	}
	mnt := []mount.Mount{{
		Source:  rootfs.Path(),
		Type:    "bind",
		Options: []string{"rbind"},
	}}
	return &constMountFactory{
		mounts: mnt,
		release: func() error {
			return s.opt.GraphDriver.Put(id)
		},
	}, nil
}

func (s *snapshotter) Remove(ctx context.Context, key string) error {
	l, err := s.getLayer(key)
	if err != nil {
		return err
	}

	var found bool
	if err := s.db.Update(func(tx *bolt.Tx) error {
		found = tx.Bucket([]byte(key)) != nil
		if found {
			id, _ := s.getGraphDriverID(key)
			tx.DeleteBucket([]byte(key))
			if id != key {
				tx.DeleteBucket([]byte(id))
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if l != nil {
		_, err := s.opt.LayerStore.Release(l)
		return err
	}

	if !found { // this happens when removing views
		return nil
	}

	return s.opt.GraphDriver.Remove(key)
}

func (s *snapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(name))
		if err != nil {
			return err
		}
		if err := b.Put(keyCommitted, []byte(key)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	return errors.Errorf("commit not implemented")
}

func (s *snapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) (snapshot.MountFactory, error) {
	return s.Mounts(ctx, parent)
}

func (s *snapshotter) Walk(ctx context.Context, fn func(context.Context, snapshots.Info) error) error {
	allKeys := map[string]struct{}{}
	commitedIDs := map[string]string{}

	if err := s.db.View(func(tx *bolt.Tx) error {
		tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			allKeys[string(name)] = struct{}{}
			v := b.Get(keyCommitted)
			if v != nil {
				commitedIDs[string(v)] = string(name)
			}
			return nil
		})
		return nil
	}); err != nil {
		return err
	}

	for k := range allKeys {
		if _, ok := commitedIDs[k]; ok {
			continue
		}
		if _, err := s.getLayer(k); err != nil {
			s.Remove(ctx, k)
			continue
		}
		info, err := s.Stat(ctx, k)
		if err != nil {
			s.Remove(ctx, k)
			continue
		}
		if err := fn(ctx, info); err != nil {
			return err
		}
	}

	return nil
}

func (s *snapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	// not implemented
	return s.Stat(ctx, info.Name)
}

func (s *snapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	return snapshots.Usage{}, nil
}

func (s *snapshotter) Close() error {
	return s.db.Close()
}

type constMountFactory struct {
	mounts  []mount.Mount
	release func() error
}

func (mf *constMountFactory) Mount() ([]mount.Mount, func() error, error) {
	release := mf.release
	if release == nil {
		release = func() error { return nil }
	}
	return mf.mounts, release, nil
}
