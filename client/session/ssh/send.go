package ssh

import (
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/client/session"
	"github.com/docker/go-connections/sockets"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

var ErrNotSupported = errors.Errorf("AuthorizeSSH not supported")

type SSHOpt struct {
	AgentSocket string
	Keys        []string // paths
}

type sshHandler struct {
	name string
	opt  SSHOpt
}

func NewSSHHandler(name string, opt SSHOpt) (session.Attachment, error) {
	if opt.AgentSocket == "" {
		opt.AgentSocket = os.Getenv("SSH_AUTH_SOCK")
	}
	h := &sshHandler{name: name, opt: opt}
	return h, nil
}

func (h *sshHandler) RegisterHandlers(fn func(id, method string) error) error {
	if err := fn(h.name, "AuthorizeSSH"); err != nil {
		return err
	}
	return nil
}
func (h *sshHandler) HandleAuthorize(opts map[string][]string, stream session.Stream) error {
	if h.opt.AgentSocket == "" {
		// TODO: create client automatically from exposed keys
	}
	if h.opt.AgentSocket == "" {
		return errors.Errorf("failed to setup SSH handler")
	}

	conn, err := net.DialTimeout("unix", h.opt.AgentSocket, 3*time.Second)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to %s", h.opt.AgentSocket)
	}

	g, ctx := errgroup.WithContext(stream.Context())

	g.Go(func() (retErr error) {
		for {
			p := &Packet{}
			if err := stream.RecvMsg(p); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if _, err := conn.Write(p.Data); err != nil {
				return err
			}
		}
	})

	g.Go(func() (retErr error) {
		for {
			buf := make([]byte, 32*1024)
			n, err := conn.Read(buf)
			switch {
			case err == io.EOF:
				return nil
			case err != nil:
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			p := &Packet{Data: buf[:n]}
			if err := stream.SendMsg(p); err != nil {
				return err
			}
		}
	})

	return g.Wait()
}

func (h *sshHandler) Handle(ctx context.Context, id, method string, opts map[string][]string, stream session.Stream) error {
	if id != h.name {
		return errors.Errorf("invalid id %s", id)
	}

	switch method {
	case "AuthorizeSSH":
		return h.HandleAuthorize(opts, stream)
	default:
		return errors.Errorf("unknown method %s", method)
	}
}

type SSHAuthProvider struct {
	caller session.Caller
	name   string
}

func NewSSHAuthProvider(name string, c session.Caller) (*SSHAuthProvider, error) {
	if !c.Supports(name, "AuthorizeSSH") {
		return nil, errors.WithStack(ErrNotSupported)
	}
	return &SSHAuthProvider{caller: c, name: name}, nil
}

// NewSSHAuthSocket creates and listens on a temporary SSH_AUTH_SOCK
func (p *SSHAuthProvider) CreateListenSocket() (string, func() error, error) {

	dir, err := ioutil.TempDir("", ".ssh-sock")
	if err != nil {
		return "", nil, nil
	}

	sockPath := filepath.Join(dir, "ssh_auth_sock")

	l, err := sockets.NewUnixSocket(sockPath, 0)
	if err != nil {
		return "", nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		conn, err := l.Accept()
		if err != nil {
			logrus.Errorf("failed to accept connection from %s: %v", p, err)
			cancel()
			return
		}

		ctx, cancel := context.WithCancel(ctx)

		stream, err := p.caller.Call(ctx, p.name, "AuthorizeSSH", nil)
		if err != nil {
			logrus.Errorf("failed to call AuthorizeSSH: %v", err)
			cancel()
			return
		}

		go func() {
			defer cancel()
			g, ctx := errgroup.WithContext(ctx)

			g.Go(func() error {
				for {
					p := &Packet{}
					if err := stream.RecvMsg(p); err != nil {
						if err == io.EOF {
							return nil
						}
						return err
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}
					if _, err := conn.Write(p.Data); err != nil {
						return err
					}
				}
			})

			g.Go(func() error {
				for {
					buf := make([]byte, 32*1024)
					n, err := conn.Read(buf)
					switch {
					case err == io.EOF:
						return nil
					case err != nil:
						return err
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}
					p := &Packet{Data: buf[:n]}
					if err := stream.SendMsg(p); err != nil {
						return err
					}
				}
			})

			if err := g.Wait(); err != nil {
				cancel()
			}
		}()
	}()

	return sockPath, func() error {
		cancel()
		err := l.Close()
		os.RemoveAll(sockPath)
		return err
	}, nil
}
