package dockerfile

import (
	"context"
	"io"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/builder/dockerfile/api"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func (bm *BuildManager) StartContextTCP(session string, rwc io.ReadWriteCloser) error {
	logrus.Debugf("TCP session")
	ds := api.NewProtoStream(rwc, rwc)
	return bm.startNewContext(context.TODO(), session, ds)
}

func (bm *BuildManager) startNewContext(mainCtx context.Context, session string, ds fsutil.Stream) error {
	logrus.Debugf("Session started: %s", session)
	defer logrus.Debugf("Session ended: %s", session)

	return bm.sessionTransport.StartSession(mainCtx, session, ds)
}

func (bm *BuildManager) StartContext(ds api.DockerfileService_StartContextServer) error {
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

	return bm.startNewContext(ds.Context(), session, ds)
}
