package builder

import (
	"io"
	"sync"

	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/pkg/tarsum"
	"github.com/pkg/errors"
)

var (
	sessions = map[string]*sessionContext{}
	sessionL sync.Mutex
)

// AttachSession attaches the given stream to a build sync
// referenced by the given session id
func AttachSession(r io.Reader, sessionID string) error {
	if sessionID == "" {
		sessionID = stringid.GenerateRandomID()
	}
	// TODO: else lookup existing sessionID

	return startSessionContext(sessionID, r)
}

func makeSessionContext(sessionID string) (ModifiableContext, error) {
	sessionL.Lock()
	session, ok := sessions[sessionID]
	sessionL.Unlock()
	if !ok {
		return nil, errors.New("session id does not exist")
	}
	return session, nil
}

type sessionContext struct {
	context   ModifiableContext
	syncC     chan struct{}
	syncErr   error
	sessionID string
}

func (c *sessionContext) Stat(path string) (string, FileInfo, error) {
	<-c.syncC
	if c.syncErr != nil {
		return "", nil, c.syncErr
	}

	return c.context.Stat(path)
}

func (c *sessionContext) Open(path string) (io.ReadCloser, error) {
	<-c.syncC
	if c.syncErr != nil {
		return nil, c.syncErr
	}

	return c.context.Open(path)
}

func (c *sessionContext) Walk(root string, walkFn WalkFunc) error {
	<-c.syncC
	if c.syncErr != nil {
		return c.syncErr
	}

	return c.context.Walk(root, walkFn)
}

func (c *sessionContext) Close() error {
	sessionL.Lock()
	if _, ok := sessions[c.sessionID]; !ok {
		// already closed elsewhere
		return nil
	}
	delete(sessions, c.sessionID)
	sessionL.Unlock()

	return c.context.Close()
}

func (c *sessionContext) Remove(path string) error {
	<-c.syncC
	if c.syncErr != nil {
		return c.syncErr
	}

	return c.context.Remove(path)
}

func startSessionContext(sessionID string, stream io.Reader) error {
	// TODO: Use sync protocol
	// TODO: 1) Use rsync
	// TODO: 2) Use continuity to first read continuity manifest
	root, err := ioutils.TempDir("", "docker-builder")
	if err != nil {
		return err
	}

	// TODO: do no use tar sum, use custom context
	tsc := &tarSumContext{root: root}

	// Make sure we clean-up upon error.  In the happy case the caller
	// is expected to manage the clean-up
	defer func() {
		if err != nil {
			tsc.Close()
		}
	}()

	context := &sessionContext{
		context:   tsc,
		syncC:     make(chan struct{}),
		sessionID: sessionID,
	}

	go func() {
		var err error
		defer func() {
			if err != nil {
				context.syncErr = err
			}
			close(context.syncC)
		}()

		decompressedStream, err := archive.DecompressStream(stream)
		if err != nil {
			err = errors.Wrap(err, "failed to decompress stream")
			return
		}

		sum, err := tarsum.NewTarSum(decompressedStream, true, tarsum.Version1)
		if err != nil {
			err = errors.Wrap(err, "failed to create tar sum")
			return
		}

		err = chrootarchive.Untar(sum, root, nil)
		if err != nil {
			err = errors.Wrap(err, "failed to untar context")
			return
		}

		tsc.sums = sum.GetSums()
	}()

	// TODO: Check doesn't exist first
	sessionL.Lock()
	sessions[sessionID] = context
	sessionL.Unlock()

	// TODO: Make this async...
	<-context.syncC
	if context.syncErr != nil {
		return context.syncErr
	}

	return nil
}
