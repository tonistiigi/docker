package builder

import (
	"io"
	"io/ioutil"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/tarsum"
	"github.com/pkg/errors"
)

// TODO: this is just a dummy context for calculating tarsums with existing methods
// TODO: move this to fscache
// TODO: implement with changefunc instead

type DirectoryContext struct {
	*tarSumContext
}

func NewDirectoryContext(root string) *DirectoryContext {
	return &DirectoryContext{
		tarSumContext: &tarSumContext{root: root},
	}
}

func (ts *DirectoryContext) Process() error {
	st := time.Now()
	defer func() {
		logrus.Debugf("DirectoryContext %v", time.Since(st))
	}()
	tarStream, err := archive.Tar(ts.root, archive.Uncompressed)
	if err != nil {
		return err
	}

	sum, err := tarsum.NewTarSum(tarStream, true, tarsum.Version1)
	if err != nil {
		return errors.Wrap(err, "failed to create tarsum")
	}
	if _, err := io.Copy(ioutil.Discard, sum); err != nil {
		return errors.Wrap(err, "failed to process tarsum")
	}
	ts.tarSumContext.sums = sum.GetSums()
	return nil
}

func (ts *DirectoryContext) Close() error {
	return nil // tarsumContext removes directory
}
