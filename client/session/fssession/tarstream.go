package fssession

import (
	"io"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

func sendTarStream(stream grpc.Stream, dir string, excludes []string, progress progressCb) error {
	a, err := archive.TarWithOptions(dir, &archive.TarOptions{
		ExcludePatterns: excludes,
	})
	if err != nil {
		return err
	}

	size := 0
	buf := make([]byte, 1<<15)
	t := new(RawBytes)
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
		if progress != nil {
			progress(size, false)
		}
	}
	if progress != nil {
		progress(size, true)
	}
	return nil
}

func recvTarStream(ds grpc.Stream, dest string, cs CacheUpdater) error {

	pr, pw := io.Pipe()

	go func() {
		var (
			err error
			t   = new(RawBytes)
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
