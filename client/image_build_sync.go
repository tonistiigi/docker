package client

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/builder/dockerfile/api"
	"github.com/docker/docker/pkg/archive"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/net/context"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type progressCb func(int, bool)

var supportedProtocols = map[string]func(fsutil.Stream, string, []string, progressCb) error{
	"tarstream": sendTarStream,
	"diffcopy":  sendDiffCopy,
}

func sendTarStream(stream fsutil.Stream, dir string, excludes []string, progress progressCb) error {
	a, err := archive.TarWithOptions(dir, &archive.TarOptions{
		ExcludePatterns: excludes,
	})
	if err != nil {
		return err
	}

	size := 0
	buf := make([]byte, 1<<15)
	t := new(api.TarContent)
	for {
		n, err := a.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		t.Content = buf[:n]

		if err := stream.SendMsg(t); err != nil {
			return err
		}
		size += n
		progress(size, false)
	}
	progress(size, true)
	return nil
}

func sendDiffCopy(stream fsutil.Stream, dir string, excludes []string, progress progressCb) error {
	return fsutil.Send(context.TODO(), stream, dir, &fsutil.WalkOpt{
		ExcludePatterns: excludes,
	}, progress)
}

func (cli *Client) ImageBuildSyncTCP(ctx context.Context, sessionID string, dir string, rewrites map[string]string, progress func(int, bool)) (error, chan error) {
	query := url.Values{}
	headers := map[string][]string{"X-Build-Session": {sessionID}}
	resp, err := cli.postHijacked(ctx, "/build-attach", query, nil, headers)
	if err != nil {
		return err, nil
	}
	s := api.NewProtoStream(resp.Reader, resp.Conn)
	errCh := make(chan error, 1)
	go func() (retErr error) {
		defer resp.Conn.Close()
		defer func() {
			if retErr != nil {
				errCh <- retErr
			}
			close(errCh)
		}()
		var tr api.TransferRequest
		if err := s.RecvMsg(&tr); err != nil {
			return err
		}

		var proto string
		for _, p := range tr.Protocol {
			if _, ok := supportedProtocols[p]; ok {
				if override := os.Getenv("BUILD_STREAM_PROTOCOL"); override != "" {
					if p != override {
						continue
					}
				}
				proto = p
				break
			}
		}
		if len(proto) == 0 {
			return errors.Errorf("could not match any protocol from server: %v", tr.Protocol)
		}

		if err := s.SendMsg(&api.TransferResponse{Protocol: proto}); err != nil {
			return err
		}

		//// TODO: Use translater?
		var excludes []string
		for k, v := range rewrites {
			if v == "" {
				excludes = append(excludes, k)
			}
		}
		return supportedProtocols[proto](s, dir, excludes, progress)

	}()

	return nil, errCh
}

// ImageBuildSync attaches to a build server to start syncing the
// provided directory. Rewrites are provided to make changes to
// what is in the given directory and what is provided to the server.
func (cli *Client) ImageBuildSync(ctx context.Context, sessionID string, dir string, rewrites map[string]string, progress func(int, bool)) (error, chan error) {
	if os.Getenv("BUILD_RAW_TCP") == "1" {
		return cli.ImageBuildSyncTCP(ctx, sessionID, dir, rewrites, progress)
	}

	query := url.Values{
		"session": []string{sessionID},
	}

	cc, err := cli.grpcClient(ctx, "/build-attach", query, nil)
	if err != nil {
		return err, nil
	}

	client := api.NewDockerfileServiceClient(cc)

	ctx = metadata.NewContext(ctx, metadata.Pairs("session", sessionID))
	contextClient, err := client.StartContext(ctx)
	if err != nil {
		return err, nil
	}

	errCh := make(chan error, 1)

	go func() (retErr error) {
		defer contextClient.CloseSend()
		defer func() {
			if retErr != nil {
				errCh <- retErr
			}
			close(errCh)
		}()
		tr, err := contextClient.Recv()
		if err != nil {
			return err
		}
		var proto string
		for _, p := range tr.Protocol {
			if _, ok := supportedProtocols[p]; ok {
				if override := os.Getenv("BUILD_STREAM_PROTOCOL"); override != "" {
					if p != override {
						continue
					}
				}
				proto = p
				break
			}
		}
		if len(proto) == 0 {
			return errors.Errorf("could not match any protocol from server: %v", tr.Protocol)
		}

		if err := contextClient.Send(&api.TransferResponse{Protocol: proto}); err != nil {
			return err
		}
		//// TODO: Use translater?
		var excludes []string
		for k, v := range rewrites {
			if v == "" {
				excludes = append(excludes, k)
			}
		}
		return supportedProtocols[proto](contextClient, dir, excludes, progress)
	}()

	return nil, errCh
}

// grpcClient returns a grpc client using the provided options for
// establishing an upgraded connection to the grpc server.
func (cli *Client) grpcClient(ctx context.Context, path string, query url.Values, headers map[string][]string) (*grpc.ClientConn, error) {
	dialer, err := cli.http2Dialer(ctx, path, query, headers)
	if err != nil {
		return nil, err
	}

	dialOpt := grpc.WithDialer(func(addr string, d time.Duration) (net.Conn, error) {
		// TODO: verify addr
		// TODO: handle duration
		return dialer()
	})

	return grpc.DialContext(ctx, "", dialOpt, grpc.WithInsecure())
}

// http2Client returns an http client which uses HTTP2 by sending
// an upgrade request to given PATH to create HTTP2 connections.
func (cli *Client) http2Client(ctx context.Context, path string, query url.Values, headers map[string][]string) (http.Client, error) {
	dialer, err := cli.http2Dialer(ctx, path, query, headers)
	if err != nil {
		return http.Client{}, err
	}

	return http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(netw, addr string, cfg *tls.Config) (net.Conn, error) {
				return dialer()
			},
		},
	}, nil
}

// http2Client returns a dialer which uses HTTP2 by sending
// an upgrade request to given PATH to create HTTP2 connections.
func (cli *Client) http2Dialer(ctx context.Context, path string, query url.Values, headers map[string][]string) (func() (net.Conn, error), error) {
	apiPath := cli.getAPIPath(path, query)
	req, err := http.NewRequest("POST", apiPath, nil)
	if err != nil {
		return nil, err
	}
	req = cli.addHeaders(req, headers)

	req.Host = cli.addr
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "h2c")

	return func() (net.Conn, error) {
		conn, err := dial(cli.proto, cli.addr, resolveTLSConfig(cli.client.Transport))
		if err != nil {
			if strings.Contains(err.Error(), "connection refused") {
				return nil, fmt.Errorf("cannot connect to the Docker daemon. Is 'docker daemon' running on this host?")
			}
			return nil, err
		}

		clientconn := httputil.NewClientConn(conn, nil)
		defer clientconn.Close()

		// Server hijacks the connection, error 'connection closed' expected
		resp, err := clientconn.Do(req)
		if resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, fmt.Errorf("unable to upgrade to HTTP2")
		}
		if err != nil {
			return nil, err
		}

		c, br := clientconn.Hijack()
		if br.Buffered() > 0 {
			// If there is buffered content, wrap the connection
			c = &hijackedConn{c, br}
		} else {
			br.Reset(nil)
		}

		return c, nil
	}, nil
}

type hijackedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *hijackedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}
