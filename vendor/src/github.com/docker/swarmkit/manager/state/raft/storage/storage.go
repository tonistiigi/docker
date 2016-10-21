package storage

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/net/context"

	"github.com/coreos/etcd/pkg/fileutil"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/snap"
	"github.com/coreos/etcd/wal"
	"github.com/coreos/etcd/wal/walpb"
	"github.com/docker/swarmkit/api"
	"github.com/docker/swarmkit/log"
	"github.com/docker/swarmkit/manager/encryption"
	"github.com/pkg/errors"
)

// ErrNoWAL is returned if there are no WALs on disk
var ErrNoWAL = errors.New("no WAL present")

// EncryptedRaftLogger saves raft data to disk
type EncryptedRaftLogger struct {
	StateDir      string
	EncryptionKey []byte

	// mutex is locked for writing only when we need to replace the wal object and snapshotter
	// object, not when we're writing snapshots or wals (in which case it's locked for reading)
	mu          sync.RWMutex
	wal         WAL
	snapshotter Snapshotter
}

// RaftDataFromDisk is returned by BootstrapFromDisk
type RaftDataFromDisk struct {
	Snapshot  *raftpb.Snapshot
	Metadata  []byte
	HardState raftpb.HardState
	Entries   []raftpb.Entry
}

// BootstrapFromDisk creates a new snapshotter and wal, and also reads the latest snapshot and WALs from disk
func (e *EncryptedRaftLogger) BootstrapFromDisk(ctx context.Context) (*RaftDataFromDisk, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	walDir := e.walDir()
	snapDir := e.snapDir()

	encoder, decoder := encryption.GetDefaults(e.EncryptionKey)
	snapFactory := NewSnapFactory(encoder, decoder)

	if !fileutil.Exist(snapDir) {
		// If snapshots created by the etcd-v2 code exist, read the latest snapshot
		// and write it encoded to the new path.  The new path prevents etc-v2 creating
		// snapshots that are visible to us, but not encoded and out of sync with our
		// WALs, after a downgrade.
		legacySnapDir := e.legacySnapDir()
		if fileutil.Exist(legacySnapDir) {
			if err := MigrateSnapshot(legacySnapDir, snapDir, OriginalSnap, snapFactory); err != nil {
				return nil, err
			}
		} else if err := os.MkdirAll(snapDir, 0700); err != nil {
			return nil, errors.Wrap(err, "failed to create snapshot directory")
		}
	}

	var (
		snapshotter Snapshotter
		walObj      WAL
		err         error
		result      = &RaftDataFromDisk{}
	)

	// Create a snapshotter and load snapshot data
	snapshotter = snapFactory.New(snapDir)
	result.Snapshot, err = snapshotter.Load()
	if err != nil && err != snap.ErrNoSnapshot {
		return nil, err
	}

	walFactory := NewWALFactory(encoder, decoder)
	var walsnap walpb.Snapshot
	if result.Snapshot != nil {
		walsnap.Index = result.Snapshot.Metadata.Index
		walsnap.Term = result.Snapshot.Metadata.Term
	}

	if !wal.Exist(walDir) {
		// If wals created by the etcd-v2 wal code exist, read the latest ones based
		// on this snapshot and encode them to wals in the new path to avoid adding
		// backwards-incompatible entries to those files.
		legacyWALDir := e.legacyWALDir()
		if !wal.Exist(legacyWALDir) {
			return nil, ErrNoWAL
		}

		if err = MigrateWALs(legacyWALDir, walDir, OriginalWAL, walFactory, walsnap, log.G(ctx)); err != nil {
			return nil, err
		}
	}

	walObj, result.Metadata, result.HardState, result.Entries, err = ReadRepairWAL(walDir, walsnap, walFactory, log.G(ctx))
	if err != nil {
		return nil, err
	}

	e.snapshotter = snapshotter
	e.wal = walObj

	return result, nil
}

// BootstrapNew creates a new snapshotter and WAL writer, expecting that there is nothing on disk
func (e *EncryptedRaftLogger) BootstrapNew(raftNode *api.RaftMember) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	encoder, decoder := encryption.GetDefaults(e.EncryptionKey)
	walFactory := NewWALFactory(encoder, decoder)

	for _, dirpath := range []string{e.walDir(), e.snapDir()} {
		if err := os.MkdirAll(dirpath, 0700); err != nil {
			return nil, errors.Wrap(err, "failed to create WAL directory")
		}
	}

	metadata, err := raftNode.Marshal()
	if err != nil {
		return nil, errors.Wrap(err, "error marshalling raft node")
	}
	e.wal, err = walFactory.Create(e.walDir(), metadata)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create WAL")
	}

	e.snapshotter = NewSnapFactory(encoder, decoder).New(e.snapDir())
	return metadata, nil
}

func (e *EncryptedRaftLogger) legacyWALDir() string {
	return filepath.Join(e.StateDir, "wal")
}

func (e *EncryptedRaftLogger) walDir() string {
	return filepath.Join(e.StateDir, "wal-v3")
}

func (e *EncryptedRaftLogger) legacySnapDir() string {
	return filepath.Join(e.StateDir, "snap")
}

func (e *EncryptedRaftLogger) snapDir() string {
	return filepath.Join(e.StateDir, "snap-v3")
}

// SaveSnapshot actually saves a given snapshot - if a new key is provided.  If a new key is provided, the key
// is actually rotated - otherwise we always try to set the encoders to our current encryption key
func (e *EncryptedRaftLogger) SaveSnapshot(snapshot raftpb.Snapshot, newKey []byte) error {
	err := func() error {
		// we want a write lock instead so we can rotate the encryption key and
		// ensure we save the WAL snapshot with the new encryption key - this way
		// we end up with all the WALs between 2 snapshots encrypted the same
		e.mu.Lock()
		defer e.mu.Unlock()

		if e.wal == nil { // if the wal exists, the snapshotter exists
			return fmt.Errorf("raft WAL has either been closed or has never been created")
		}

		walsnap := walpb.Snapshot{
			Index: snapshot.Metadata.Index,
			Term:  snapshot.Metadata.Term,
		}

		if newKey == nil {
			// never mind with rotating the encoders - just save the WAL snapshot
			return e.wal.SaveSnapshot(walsnap)
		}

		encoder, decoder := encryption.GetDefaults(newKey)
		wrapped, ok := e.wal.(*wrappedWAL)
		reset := func() {}
		if ok {
			// we don't want to have to close the WAL and bootstrap a new one,
			// so just rotate the encoders out from under it.  We already
			// have a lock on writing to snapshots and WALs
			oldEncoder := wrapped.encoder
			oldDecoder := wrapped.decoder
			reset = func() {
				wrapped.encoder = oldEncoder
				wrapped.decoder = oldDecoder
			}
			wrapped.encoder = encoder
			wrapped.decoder = decoder

		}
		if err := e.wal.SaveSnapshot(walsnap); err != nil {
			reset()
			return err
		}
		e.snapshotter = NewSnapFactory(encoder, decoder).New(e.snapDir())
		e.EncryptionKey = newKey
		return nil
	}()

	if err != nil {
		return err
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if err := e.snapshotter.SaveSnap(snapshot); err != nil {
		return err
	}
	if err := e.wal.ReleaseLockTo(snapshot.Metadata.Index); err != nil {
		return err
	}
	return nil
}

// GC garbage collects snapshots and wals older than the provided index and term
func (e *EncryptedRaftLogger) GC(index uint64, term uint64, keepOldSnapshots uint64) error {
	// Delete any older snapshots
	curSnapshot := fmt.Sprintf("%016x-%016x%s", term, index, ".snap")

	snapshots, err := ListSnapshots(e.snapDir())
	if err != nil {
		return err
	}

	// Ignore any snapshots that are older than the current snapshot.
	// Delete the others. Rather than doing lexical comparisons, we look
	// at what exists before/after the current snapshot in the slice.
	// This means that if the current snapshot doesn't appear in the
	// directory for some strange reason, we won't delete anything, which
	// is the safe behavior.
	curSnapshotIdx := -1
	var (
		removeErr      error
		oldestSnapshot string
	)

	for i, snapFile := range snapshots {
		if curSnapshotIdx >= 0 && i > curSnapshotIdx {
			if uint64(i-curSnapshotIdx) > keepOldSnapshots {
				err := os.Remove(filepath.Join(e.snapDir(), snapFile))
				if err != nil && removeErr == nil {
					removeErr = err
				}
				continue
			}
		} else if snapFile == curSnapshot {
			curSnapshotIdx = i
		}
		oldestSnapshot = snapFile
	}

	if removeErr != nil {
		return removeErr
	}

	// Remove any WAL files that only contain data from before the oldest
	// remaining snapshot.

	if oldestSnapshot == "" {
		return nil
	}

	// Parse index out of oldest snapshot's filename
	var snapTerm, snapIndex uint64
	_, err = fmt.Sscanf(oldestSnapshot, "%016x-%016x.snap", &snapTerm, &snapIndex)
	if err != nil {
		return errors.Wrapf(err, "malformed snapshot filename %s", oldestSnapshot)
	}

	wals, err := ListWALs(e.walDir())
	if err != nil {
		return err
	}

	found := false
	deleteUntil := -1

	for i, walName := range wals {
		var walSeq, walIndex uint64
		_, err = fmt.Sscanf(walName, "%016x-%016x.wal", &walSeq, &walIndex)
		if err != nil {
			return errors.Wrapf(err, "could not parse WAL name %s", walName)
		}

		if walIndex >= snapIndex {
			deleteUntil = i - 1
			found = true
			break
		}
	}

	// If all WAL files started with indices below the oldest snapshot's
	// index, we can delete all but the newest WAL file.
	if !found && len(wals) != 0 {
		deleteUntil = len(wals) - 1
	}

	for i := 0; i < deleteUntil; i++ {
		walPath := filepath.Join(e.walDir(), wals[i])
		l, err := fileutil.TryLockFile(walPath, os.O_WRONLY, fileutil.PrivateFileMode)
		if err != nil {
			return errors.Wrapf(err, "could not lock old WAL file %s for removal", wals[i])
		}
		err = os.Remove(walPath)
		l.Close()
		if err != nil {
			return errors.Wrapf(err, "error removing old WAL file %s", wals[i])
		}
	}

	return nil
}

// SaveEntries saves only entries to disk
func (e *EncryptedRaftLogger) SaveEntries(st raftpb.HardState, entries []raftpb.Entry) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.wal == nil {
		return fmt.Errorf("raft WAL has either been closed or has never been created")
	}
	return e.wal.Save(st, entries)
}

// Close closes the logger - it will have to be bootstrapped again to start writing
func (e *EncryptedRaftLogger) Close(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.wal != nil {
		if err := e.wal.Close(); err != nil {
			log.G(ctx).WithError(err).Error("error closing raft WAL")
		}
	}

	e.wal = nil
	e.snapshotter = nil
}

// Clear closes the existing WAL and moves away the WAL and snapshot.
func (e *EncryptedRaftLogger) Clear(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.wal != nil {
		if err := e.wal.Close(); err != nil {
			log.G(ctx).WithError(err).Error("error closing raft WAL")
		}
	}
	e.snapshotter = nil

	newWALDir, err := ioutil.TempDir(e.StateDir, "wal.")
	if err != nil {
		return err
	}
	err = os.Rename(e.walDir(), newWALDir)
	if err != nil {
		return err
	}

	newSnapDir, err := ioutil.TempDir(e.StateDir, "snap.")
	if err != nil {
		return err
	}
	err = os.Rename(e.snapDir(), newSnapDir)
	if err != nil {
		return err
	}

	return nil
}
