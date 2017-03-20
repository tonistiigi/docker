package builder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/chrootarchive"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/symlink"
	"github.com/docker/docker/pkg/tarsum"
)

type tarSumContext struct {
	root string
	sums tarsum.FileInfoSums
}

func (c *tarSumContext) Close() error {
	return os.RemoveAll(c.root)
}

func convertPathError(err error, cleanpath string) error {
	if err, ok := err.(*os.PathError); ok {
		err.Path = cleanpath
		return err
	}
	return err
}

// MakeTarSumContext returns a build Context from a tar stream.
//
// It extracts the tar stream to a temporary folder that is deleted as soon as
// the Context is closed.
// As the extraction happens, a tarsum is calculated for every file, and the set of
// all those sums then becomes the source of truth for all operations on this Context.
//
// Closing tarStream has to be done by the caller.
func MakeTarSumContext(tarStream io.Reader) (ModifiableContext, error) {
	root, err := ioutils.TempDir("", "docker-builder")
	if err != nil {
		return nil, err
	}

	tsc := &tarSumContext{root: root}

	// Make sure we clean-up upon error.  In the happy case the caller
	// is expected to manage the clean-up
	defer func() {
		if err != nil {
			tsc.Close()
		}
	}()

	decompressedStream, err := archive.DecompressStream(tarStream)
	if err != nil {
		return nil, err
	}

	sum, err := tarsum.NewTarSum(decompressedStream, true, tarsum.Version1)
	if err != nil {
		return nil, err
	}

	err = chrootarchive.Untar(sum, root, nil)
	if err != nil {
		return nil, err
	}

	tsc.sums = sum.GetSums()

	return tsc, nil
}

func (c *tarSumContext) Root() string {
	return c.root
}

func (c *tarSumContext) normalize(path string) (cleanpath, fullpath string, err error) {
	cleanpath = filepath.Clean(string(os.PathSeparator) + path)[1:]
	fullpath, err = symlink.FollowSymlinkInScope(filepath.Join(c.root, path), c.root)
	if err != nil {
		return "", "", fmt.Errorf("Forbidden path outside the build context: %s (%s)", path, fullpath)
	}
	_, err = os.Lstat(fullpath)
	if err != nil {
		return "", "", convertPathError(err, path)
	}
	return
}

func (c *tarSumContext) Remove(path string) error {
	_, fullpath, err := c.normalize(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(fullpath)
}

func (c *tarSumContext) Hash(path string) (string, error) {
	cleanpath, fullpath, err := c.normalize(path)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(c.root, fullpath)
	if err != nil {
		return "", convertPathError(err, cleanpath)
	}

	// Use the checksum of the followed path(not the possible symlink) because
	// this is the file that is actually copied.
	if tsInfo := c.sums.GetFile(filepath.ToSlash(rel)); tsInfo != nil {
		return tsInfo.Sum(), nil
	}
	// We set sum to path by default for the case where GetFile returns nil.
	// The usual case is if relative path is empty.
	return path, nil // backwards compat TODO: see if really needed
}
