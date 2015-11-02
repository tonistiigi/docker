package daemon

import (
	"strings"

	derr "github.com/docker/docker/errors"
	"github.com/docker/docker/pkg/parsers"
	tagpkg "github.com/docker/docker/tag"
)

func (d *Daemon) imageNotExistToErrcode(err error) error {
	if dne, isDNE := err.(ErrImageDoesNotExist); isDNE {
		if strings.Contains(dne.RefOrID, "@") {
			return derr.ErrorCodeNoSuchImageHash.WithArgs(dne.RefOrID)
		}
		img, tag := parsers.ParseRepositoryTag(dne.RefOrID)
		if tag == "" {
			tag = tagpkg.DefaultTag
		}
		return derr.ErrorCodeNoSuchImageTag.WithArgs(img, tag)
	}
	return err
}
