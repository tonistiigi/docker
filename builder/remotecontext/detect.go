package remotecontext

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/builder"
	"github.com/docker/docker/builder/dockerignore"
	"github.com/docker/docker/pkg/fileutils"
	"github.com/docker/docker/pkg/httputils"
	"github.com/docker/docker/pkg/symlink"
	"github.com/docker/docker/pkg/urlutil"
)

// Detect returns a context and dockerfile from remote location or local
// archive. progressReader is only used if remoteURL is actually a URL
// (not empty, and not a Git endpoint).
func Detect(ctx context.Context, remoteURL string, dockerfilePath string, r io.ReadCloser, progressReader func(in io.ReadCloser) io.ReadCloser) (remote builder.Source, dockerfile io.ReadCloser, err error) {
	// TODO: return parsed dockerfile directly from here after rebase of #27270
	switch {
	case remoteURL == "":
		remote, dockerfile, err = newArchiveRemote(r, dockerfilePath)
	case urlutil.IsGitURL(remoteURL):
		remote, dockerfile, err = newGitRemote(remoteURL, dockerfilePath)
	case urlutil.IsURL(remoteURL):
		remote, dockerfile, err = newURLRemote(remoteURL, dockerfilePath, progressReader)
	default:
		err = fmt.Errorf("remoteURL (%s) could not be recognized as URL", remoteURL)
	}
	return
}

func newArchiveRemote(rc io.ReadCloser, dockerfilePath string) (builder.Source, io.ReadCloser, error) {
	c, err := MakeTarSumContext(rc)
	if err != nil {
		return nil, nil, err
	}

	return withDockerfileFromContext(c.(modifiableContext), dockerfilePath)
}

func withDockerfileFromContext(c modifiableContext, dockerfilePath string) (builder.Source, io.ReadCloser, error) {
	df, err := openAt(c, dockerfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			if dockerfilePath == builder.DefaultDockerfileName {
				lowercase := strings.ToLower(dockerfilePath)
				if _, err := StatAt(c, lowercase); err == nil {
					return withDockerfileFromContext(c, lowercase)
				}
			}
			return nil, nil, fmt.Errorf("Cannot locate specified Dockerfile: %s", dockerfilePath) // backwards compatible error
		}
		c.Close()
		return nil, nil, err
	}

	if err := removeDockerfile(c, dockerfilePath); err != nil {
		c.Close()
		return nil, nil, err
	}

	return c, df, nil
}

func newGitRemote(gitURL string, dockerfilePath string) (builder.Source, io.ReadCloser, error) {
	c, err := MakeGitContext(gitURL) // TODO: change this to NewLazyContext
	if err != nil {
		return nil, nil, err
	}
	return withDockerfileFromContext(c.(modifiableContext), dockerfilePath)
}

func newURLRemote(url string, dockerfilePath string, progressReader func(in io.ReadCloser) io.ReadCloser) (builder.Source, io.ReadCloser, error) {
	var dockerfile io.ReadCloser
	dockerfileFoundErr := errors.New("found-dockerfile")
	c, err := MakeRemoteContext(url, map[string]func(io.ReadCloser) (io.ReadCloser, error){
		httputils.MimeTypes.TextPlain: func(rc io.ReadCloser) (io.ReadCloser, error) {
			dockerfile = rc
			return nil, dockerfileFoundErr
		},
		// fallback handler (tar context)
		"": func(rc io.ReadCloser) (io.ReadCloser, error) {
			return progressReader(rc), nil
		},
	})
	if err != nil {
		if err == dockerfileFoundErr {
			return nil, dockerfile, nil
		}
		return nil, nil, err
	}

	return withDockerfileFromContext(c.(modifiableContext), dockerfilePath)
}

func removeDockerfile(c modifiableContext, filesToRemove ...string) error {
	f, err := openAt(c, ".dockerignore")
	// Note that a missing .dockerignore file isn't treated as an error
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	}
	excludes, _ := dockerignore.ReadAll(f)
	f.Close()
	filesToRemove = append([]string{".dockerignore"}, filesToRemove...)
	for _, fileToRemove := range filesToRemove {
		if rm, _ := fileutils.Matches(fileToRemove, excludes); rm {
			c.Remove(fileToRemove)
		}
	}
	return nil
}

func openAt(remote builder.Source, path string) (*os.File, error) {
	fullPath, err := FullPath(remote, path)
	if err != nil {
		return nil, err
	}
	return os.Open(fullPath)
}

// StatAt is a helper for calling Stat on a path from a source
func StatAt(remote builder.Source, path string) (os.FileInfo, error) {
	fullPath, err := FullPath(remote, path)
	if err != nil {
		return nil, err
	}
	return os.Stat(fullPath)
}

// FullPath is a helper for getting a full path for a path from a source
func FullPath(remote builder.Source, path string) (string, error) {
	fullPath, err := symlink.FollowSymlinkInScope(filepath.Join(remote.Root(), path), remote.Root())
	if err != nil {
		return "", fmt.Errorf("Forbidden path outside the build context: %s (%s)", path, fullPath) // backwards compat with old error
	}
	return fullPath, nil
}
