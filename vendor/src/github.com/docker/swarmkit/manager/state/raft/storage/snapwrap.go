package storage

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coreos/etcd/pkg/fileutil"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/snap"
	"github.com/docker/swarmkit/manager/encryption"
	"github.com/pkg/errors"
)

// This package wraps the github.com/coreos/etcd/snap package, and encodes
// the bytes of whatever snapshot is passed to it, and decodes the bytes of
// whatever snapshot it reads.

// Snapshotter is the interface presented by github.com/coreos/etcd/snap.Snapshotter that we depend upon
type Snapshotter interface {
	SaveSnap(snapshot raftpb.Snapshot) error
	Load() (*raftpb.Snapshot, error)
}

// SnapFactory provides an interface for the different ways to get a Snapshotter object.
// For instance, the etcd/snap package itself provides this
type SnapFactory interface {
	New(dirpath string) Snapshotter
}

var _ Snapshotter = &wrappedSnap{}
var _ Snapshotter = &snap.Snapshotter{}
var _ SnapFactory = snapCryptor{}

// wrappedSnap wraps a github.com/coreos/etcd/snap.Snapshotter, and handles
// encoding/decoding.
type wrappedSnap struct {
	*snap.Snapshotter
	encoder encryption.Encoder
	decoder encryption.Decoder
}

// SaveSnap encodes the snapshot data (if an encoder is exists) before passing it onto the
// wrapped snap.Snapshotter's SaveSnap function.
func (s *wrappedSnap) SaveSnap(snapshot raftpb.Snapshot) error {
	toWrite := snapshot
	var err error
	toWrite.Data, err = encryption.Encode(snapshot.Data, s.encoder)
	if err != nil {
		return err
	}
	return s.Snapshotter.SaveSnap(toWrite)
}

// Load decodes the snapshot data (if a decoder is exists) after reading it using the
// wrapped snap.Snapshotter's Load function.
func (s *wrappedSnap) Load() (*raftpb.Snapshot, error) {
	snapshot, err := s.Snapshotter.Load()
	if err != nil {
		return nil, err
	}
	snapshot.Data, err = encryption.Decode(snapshot.Data, s.decoder)
	if err != nil {
		return nil, err
	}

	return snapshot, nil
}

// snapCryptor is an object that provides the same functions as `etcd/wal`
// and `etcd/snap` that we need to open a WAL object or Snapshotter object
type snapCryptor struct {
	encoder encryption.Encoder
	decoder encryption.Decoder
}

// NewSnapFactory returns a new object that can read from and write to encrypted
// snapshots on disk
func NewSnapFactory(encoder encryption.Encoder, decoder encryption.Decoder) SnapFactory {
	return snapCryptor{
		encoder: encoder,
		decoder: decoder,
	}
}

// NewSnapshotter returns a new Snapshotter with the given encoders and decoders
func (sc snapCryptor) New(dirpath string) Snapshotter {
	return &wrappedSnap{
		Snapshotter: snap.New(dirpath),
		encoder:     sc.encoder,
		decoder:     sc.decoder,
	}
}

type originalSnap struct{}

func (o originalSnap) New(dirpath string) Snapshotter {
	return snap.New(dirpath)
}

// OriginalSnap is the original `snap` package as an implemntation of the SnapFactory interface
var OriginalSnap = originalSnap{}

// MigrateSnapshot reads the latest existing snapshot from one directory, encoded one way, and writes
// it to a new directory, encoded a different way
func MigrateSnapshot(oldDir, newDir string, oldFactory, newFactory SnapFactory) error {
	// use temporary snaphot directory so initialization appears atomic
	tmpdirpath := filepath.Clean(newDir) + ".tmp"
	if fileutil.Exist(tmpdirpath) {
		if err := os.RemoveAll(tmpdirpath); err != nil {
			return errors.Wrap(err, "could not remove temporary snapshot directory")
		}
	}
	if err := fileutil.CreateDirAll(tmpdirpath); err != nil {
		return errors.Wrap(err, "could not create temporary snapshot directory")
	}

	oldSnapshotter := oldFactory.New(oldDir)
	snapshot, err := oldSnapshotter.Load()
	if err != nil {
		return err
	}

	tmpSnapshotter := newFactory.New(tmpdirpath)

	// write the new snapshot to the temporary location
	if err = tmpSnapshotter.SaveSnap(*snapshot); err != nil {
		return err
	}

	return os.Rename(tmpdirpath, newDir)
}

// ListSnapshots lists all the snapshot files in a particular directory
func ListSnapshots(dirpath string) ([]string, error) {
	dirents, err := ioutil.ReadDir(dirpath)
	if err != nil {
		return nil, err
	}

	var snapshots []string
	for _, dirent := range dirents {
		if strings.HasSuffix(dirent.Name(), ".snap") {
			snapshots = append(snapshots, dirent.Name())
		}
	}

	// Sort snapshot filenames in reverse lexical order
	sort.Sort(sort.Reverse(sort.StringSlice(snapshots)))
	return snapshots, nil
}
