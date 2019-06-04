// +build !linux

package vfs // import "github.com/docker/docker/daemon/graphdriver/vfs"

import "github.com/docker/docker/pkg/archive"

func dirCopy(srcDir, dstDir string) error {
	return archive.NewArchiver(nil).CopyWithTar(srcDir, dstDir)
}
