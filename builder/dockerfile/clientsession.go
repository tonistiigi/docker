package dockerfile

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/builder/dockerfile/api"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/net/context"
)

const ClientSessionTransportName = "client-session"
const sessionConnectTimeout = 5 * time.Second

// ClientSessionTransport is a transport for copying files from docker client
// to the daemon.
type ClientSessionTransport struct {
	mu       sync.Mutex
	c        *sync.Cond
	sessions map[string]*sessionCallback
}

// Stream defines an implementation from transfering data.
type Stream interface {
	RecvMsg(interface{}) error
	SendMsg(m interface{}) error
}

// NewClientSessionTransport returns new ClientSessionTransport instance
func NewClientSessionTransport() *ClientSessionTransport {
	cst := &ClientSessionTransport{
		sessions: make(map[string]*sessionCallback),
	}
	cst.c = sync.NewCond(&cst.mu)
	return cst
}

// StartSession registers a stream being used for transfers for a session
func (cst *ClientSessionTransport) StartSession(ctx context.Context, session string, stream Stream) error {
	cst.mu.Lock()
	if _, ok := cst.sessions[session]; ok {
		cst.mu.Unlock()
		return errors.Errorf("already active session for %s", session)
	}

	copyCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb := &sessionCallback{
		close:  cancel,
		stream: stream,
	}
	cst.sessions[session] = cb
	cst.mu.Unlock()
	cst.c.Broadcast()

	select {
	case <-copyCtx.Done():
	case <-ctx.Done():
	}

	cst.mu.Lock()
	delete(cst.sessions, session)
	cst.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Copy copies data from remote to a destination directory.
func (cst *ClientSessionTransport) Copy(ctx context.Context, id RemoteIdentifier, dest string, cs fsutil.ChangeFunc) error {
	csi, ok := id.(*ClientSessionIdentifier)
	if !ok {
		return errors.New("invalid identifier for client session")
	}

	ctx, cancel := context.WithTimeout(ctx, sessionConnectTimeout)
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			cst.c.Broadcast()
		}
	}()

	var session *sessionCallback

	cst.mu.Lock()
	for {
		select {
		case <-ctx.Done():
			cst.mu.Unlock()
			return errors.Wrapf(ctx.Err(), "no active session for %s", id.Key())
		default:
		}
		var ok bool
		session, ok = cst.sessions[id.Key()]
		if !ok || session.handled {
			cst.c.Wait()
			continue
		}
		session.handled = true
		cst.mu.Unlock()
		break
	}

	defer session.close()

	if err := receiveContext(ctx, csi.srcPaths, session.stream, dest, cs); err != nil {
		return err
	}
	return nil
}

type sessionCallback struct {
	close   func()
	stream  fsutil.Stream
	handled bool // remove this and mux substreams
}

// ClientSessionIdentifier is an identifier that can be used for requesting
// files from remote client
type ClientSessionIdentifier struct {
	session  string
	srcPaths []string
}

// NewClientSessionIdentifier returns new ClientSessionIdentifier instance
func NewClientSessionIdentifier(session string, sources []string) *ClientSessionIdentifier {
	return &ClientSessionIdentifier{session: session, srcPaths: sources}
}

// Transport returns transport identifier for remote identifier
func (csi *ClientSessionIdentifier) Transport() string {
	return ClientSessionTransportName
}

// SharedKey returns shared key for remote identifier. Shared key is used
// for finding the base for a repeated transfer.
func (csi *ClientSessionIdentifier) SharedKey() string {
	parts := strings.SplitN(csi.session, ",", 2)
	return parts[0]
}

// Key returns unique key for remote identifier. Requests with same key return
// same data.
func (csi *ClientSessionIdentifier) Key() string {
	return csi.session
}

var supportedProtocols = map[string]func(context.Context, fsutil.Stream, string, fsutil.ChangeFunc) error{
	"tarstream": receiveTarStream,
	"diffcopy":  receiveDiffCopy,
}

func receiveContext(ctx context.Context, src []string, ds Stream, dest string, cs fsutil.ChangeFunc) error {
	req := &api.TransferRequest{
		Protocol: []string{"tarstream", "diffcopy"},
		Source:   src,
	}
	if err := ds.SendMsg(req); err != nil {
		return err
	}

	resp := new(api.TransferResponse)
	if err := ds.RecvMsg(resp); err != nil {
		if err == io.EOF {
			return errors.Errorf("no transferresponse received")
		}
		return err
	}
	p, ok := supportedProtocols[resp.Protocol]
	if !ok {
		return errors.Errorf("unsupported protocol %s", resp.Protocol)
	}

	return p(ctx, ds, dest, cs)
}

func receiveTarStream(ctx context.Context, ds fsutil.Stream, dest string, cs fsutil.ChangeFunc) error {
	pr, pw := io.Pipe()

	go func() {
		var (
			err error
			t   = new(api.TarContent)
		)
		for {
			if err = ds.RecvMsg(t); err != nil {
				if err == io.EOF {
					err = nil
				}
				break
			}
			_, err = pw.Write(t.Content)
			if err != nil {
				break
			}
		}
		if err = pw.CloseWithError(err); err != nil {
			logrus.Errorf("failed to close tar transfer pipe")
		}
	}()

	decompressedStream, err := archive.DecompressStream(pr)
	if err != nil {
		return errors.Wrap(err, "failed to decompress stream")
	}

	if err := chrootarchive.Untar(decompressedStream, dest, nil); err != nil {
		return errors.Wrap(err, "failed to untar context")
	}
	return nil
}

func receiveDiffCopy(ctx context.Context, ds fsutil.Stream, dest string, cs fsutil.ChangeFunc) error {
	st := time.Now()
	defer func() {
		logrus.Debugf("diffcopy took: %v", time.Since(st))
	}()
	err := fsutil.Receive(ctx, ds, dest, cs)
	if err == nil {
		cs(-1, "", nil, nil) // remove
	}
	return err
}
