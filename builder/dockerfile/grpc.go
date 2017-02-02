package dockerfile

import (
	"io"
	"net/http"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerfile/api"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func (bm *BuildManager) BuildServer(ctx context.Context) http.Handler {
	gs := grpc.NewServer()
	bs := &dockerfileService{
		manager: bm,
	}
	api.RegisterDockerfileServiceServer(gs, bs)
	return gs
}

type dockerfileService struct {
	manager *BuildManager
}

func (s *dockerfileService) StartContext(ds api.DockerfileService_StartContextServer) error {
	md, ok := metadata.FromContext(ds.Context())
	if !ok {
		return grpc.Errorf(codes.InvalidArgument, "missing metadata")
	}
	var session string
	if sessionH, ok := md["session"]; ok && len(sessionH) > 0 {
		session = sessionH[0]
	} else {
		return grpc.Errorf(codes.InvalidArgument, "missing required header: %q", "session")
	}
	req := &api.TransferRequest{
		Protocol: []string{"tarstream"},
		Source:   []string{"/"},
	}
	if err := ds.Send(req); err != nil {
		return err
	}

	resp, err := ds.Recv()
	if err != nil {
		if err == io.EOF {
			return grpc.Errorf(codes.InvalidArgument, "no transfer sent")
		}
		return err
	}
	if resp.Protocol != "tarstream" {
		return grpc.Errorf(codes.InvalidArgument, "unsupported protocol")
	}

	logrus.Debugf("Session started: %s", session)

	writeDone := make(chan struct{})
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
			logrus.Errorf("Failed to close tar transfer pipe")
		}

		close(writeDone)
	}()

	if err := builder.AttachSession(pr, session); err != nil {
		return err
	}

	<-writeDone

	return nil

}
