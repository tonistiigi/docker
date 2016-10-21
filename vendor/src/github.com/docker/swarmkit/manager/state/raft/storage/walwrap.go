package storage

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/pkg/fileutil"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/wal"
	"github.com/coreos/etcd/wal/walpb"
	"github.com/docker/swarmkit/manager/encryption"
	"github.com/pkg/errors"
)

// This package wraps the github.com/coreos/etcd/wal package, and encodes
// the bytes of whatever entry is passed to it, and decodes the bytes of
// whatever entry it reads.

// WAL is the interface presented by github.com/coreos/etcd/wal.WAL that we depend upon
type WAL interface {
	ReadAll() ([]byte, raftpb.HardState, []raftpb.Entry, error)
	ReleaseLockTo(index uint64) error
	Close() error
	Save(st raftpb.HardState, ents []raftpb.Entry) error
	SaveSnapshot(e walpb.Snapshot) error
}

// WALFactory provides an interface for the different ways to get a WAL object.
// For instance, the etcd/wal package itself provides this
type WALFactory interface {
	Create(dirpath string, metadata []byte) (WAL, error)
	Open(dirpath string, walsnap walpb.Snapshot) (WAL, error)
}

var _ WAL = &wrappedWAL{}
var _ WAL = &wal.WAL{}
var _ WALFactory = walCryptor{}

// wrappedWAL wraps a github.com/coreos/etcd/wal.WAL, and handles encoding/decoding
type wrappedWAL struct {
	*wal.WAL
	encoder encryption.Encoder
	decoder encryption.Decoder
}

// ReadAll wraps the wal.WAL.ReadAll() function, but it first checks to see if the
// metadata indicates that the entries are encoded, and if so, decodes them.
func (w *wrappedWAL) ReadAll() ([]byte, raftpb.HardState, []raftpb.Entry, error) {
	metadata, state, ents, err := w.WAL.ReadAll()
	if err != nil {
		return metadata, state, ents, err
	}
	for i, ent := range ents {
		ents[i].Data, err = encryption.Decode(ent.Data, w.decoder)
		if err != nil {
			return nil, raftpb.HardState{}, nil, err
		}
	}

	return metadata, state, ents, nil
}

// Save encodes the entry data (if an encoder is exists) before passing it onto the
// wrapped wal.WAL's Save function.
func (w *wrappedWAL) Save(st raftpb.HardState, ents []raftpb.Entry) error {
	var writeEnts []raftpb.Entry
	for _, ent := range ents {
		data, err := encryption.Encode(ent.Data, w.encoder)
		if err != nil {
			return err
		}
		writeEnts = append(writeEnts, raftpb.Entry{
			Index: ent.Index,
			Term:  ent.Term,
			Type:  ent.Type,
			Data:  data,
		})
	}

	return w.WAL.Save(st, writeEnts)
}

// walCryptor is an object that provides the same functions as `etcd/wal`
// and `etcd/snap` that we need to open a WAL object or Snapshotter object
type walCryptor struct {
	encoder encryption.Encoder
	decoder encryption.Decoder
}

// NewWALFactory returns an object that can be used to produce objects that
// will read from and write to encrypted WALs on disk.
func NewWALFactory(encoder encryption.Encoder, decoder encryption.Decoder) WALFactory {
	return walCryptor{
		encoder: encoder,
		decoder: decoder,
	}
}

// Create returns a new WAL object with the given encoders and decoders.
func (wc walCryptor) Create(dirpath string, metadata []byte) (WAL, error) {
	w, err := wal.Create(dirpath, metadata)
	if err != nil {
		return nil, err
	}
	return &wrappedWAL{
		WAL:     w,
		encoder: wc.encoder,
		decoder: wc.decoder,
	}, nil
}

// Open returns a new WAL object with the given encoders and decoders.
func (wc walCryptor) Open(dirpath string, snap walpb.Snapshot) (WAL, error) {
	w, err := wal.Open(dirpath, snap)
	if err != nil {
		return nil, err
	}
	return &wrappedWAL{
		WAL:     w,
		encoder: wc.encoder,
		decoder: wc.decoder,
	}, nil
}

type originalWAL struct{}

func (o originalWAL) Create(dirpath string, metadata []byte) (WAL, error) {
	return wal.Create(dirpath, metadata)
}
func (o originalWAL) Open(dirpath string, walsnap walpb.Snapshot) (WAL, error) {
	return wal.Open(dirpath, walsnap)
}

// OriginalWAL is the original `wal` package as an implemntation of the WALFactory interface
var OriginalWAL = originalWAL{}

// ReadRepairWAL opens a WAL for reading, and attempts to read it.  If we can't read it, attempts to repair
// and read again.
func ReadRepairWAL(walDir string, walsnap walpb.Snapshot, factory WALFactory, logger *logrus.Entry) (
	WAL, []byte, raftpb.HardState, []raftpb.Entry, error) {
	var (
		reader   WAL
		metadata []byte
		st       raftpb.HardState
		ents     []raftpb.Entry
		err      error
	)
	repaired := false
	for {
		if reader, err = factory.Open(walDir, walsnap); err != nil {
			return nil, nil, raftpb.HardState{}, nil, errors.Wrap(err, "failed to open WAL")
		}
		if metadata, st, ents, err = reader.ReadAll(); err != nil {
			if closeErr := reader.Close(); closeErr != nil {
				return nil, nil, raftpb.HardState{}, nil, closeErr
			}
			if _, ok := err.(encryption.ErrCannotDecode); ok {
				return nil, nil, raftpb.HardState{}, nil, err
			}
			// we can only repair ErrUnexpectedEOF and we never repair twice.
			if repaired || err != io.ErrUnexpectedEOF {
				return nil, nil, raftpb.HardState{}, nil, errors.Wrap(err, "irreparable WAL error")
			}
			if !wal.Repair(walDir) {
				return nil, nil, raftpb.HardState{}, nil, errors.Wrap(err, "WAL error cannot be repaired")
			}
			if logger != nil {
				logger.WithError(err).Info("repaired WAL error")
			}
			repaired = true
			continue
		}
		break
	}
	return reader, metadata, st, ents, nil
}

// MigrateWALs reads existing WALs (from a particular snapshot and beyond) from one directory, encoded one way,
// and writes them to a new directory, encoded a different way
func MigrateWALs(oldDir, newDir string, oldFactory, newFactory WALFactory, snapshot walpb.Snapshot, logger *logrus.Entry) error {
	// keep temporary wal directory so WAL initialization appears atomic
	tmpdirpath := filepath.Clean(newDir) + ".tmp"
	if fileutil.Exist(tmpdirpath) {
		if err := os.RemoveAll(tmpdirpath); err != nil {
			return errors.Wrap(err, "could not remove temporary WAL directory")
		}
	}
	if err := fileutil.CreateDirAll(tmpdirpath); err != nil {
		return errors.Wrap(err, "could not create temporary WAL directory")
	}

	oldReader, metadata, st, ents, err := ReadRepairWAL(oldDir, snapshot, oldFactory, logger)
	if err != nil {
		return err
	}
	oldReader.Close()

	tmpReader, err := newFactory.Create(tmpdirpath, metadata)
	if err != nil {
		return errors.Wrap(err, "could not create new WAL in temporary WAL directory")
	}
	defer tmpReader.Close()

	if err := tmpReader.SaveSnapshot(snapshot); err != nil {
		return errors.Wrap(err, "could not write WAL snapshot in temporary directory")
	}

	if err := tmpReader.Save(st, ents); err != nil {
		return errors.Wrap(err, "could not migrate WALs to temporary directory")
	}

	return os.Rename(tmpdirpath, newDir)
}

// ListWALs lists all the wals in a directory
func ListWALs(dirpath string) ([]string, error) {
	dirents, err := ioutil.ReadDir(dirpath)
	if err != nil {
		return nil, err
	}

	var wals []string
	for _, dirent := range dirents {
		if strings.HasSuffix(dirent.Name(), ".wal") {
			wals = append(wals, dirent.Name())
		}
	}

	// Sort WAL filenames in lexical order
	sort.Sort(sort.StringSlice(wals))
	return wals, nil
}
