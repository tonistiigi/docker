package libcontainerd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/docker/docker/pkg/idtools"
	"github.com/opencontainers/specs"
)

// CreateContainer defines methods for container creation.
type CreateContainer interface {
	Create(id string, spec specs.LinuxSpec, options ...CreateOption) error
}

func (c *client) prepareBundleDir(uid, gid int) (string, error) {
	root, err := filepath.Abs(c.remote.stateDir)
	if err != nil {
		return "", err
	}
	if uid == 0 && gid == 0 {
		return root, nil
	}
	p := string(filepath.Separator)
	for _, d := range strings.Split(root, string(filepath.Separator))[1:] {
		p = filepath.Join(p, d)
		fi, err := os.Stat(p)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if os.IsNotExist(err) || fi.Mode()&1 == 0 {
			p = fmt.Sprintf("%s.%d.%d", p, uid, gid)
			if err := idtools.MkdirAs(p, 0700, uid, gid); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
	}
	return p, nil
}

func (c *client) Create(id string, spec specs.LinuxSpec, options ...CreateOption) (err error) {
	c.lock(id)
	defer c.unlock(id)

	if c, err := c.getContainer(id); err == nil {
		if c.restarting { // docker doesn't actually call start if restart is on atm, but probably should in the future
			c.restartManager.Cancel()
			c.clean()
		} else {
			return fmt.Errorf("Container %s is aleady active", id)
		}
	}

	uid, gid, err := getRootIDs(spec)
	if err != nil {
		return err
	}
	dir, err := c.prepareBundleDir(uid, gid)
	if err != nil {
		return err
	}

	container := c.newContainer(filepath.Join(dir, id), options...)
	if err := container.clean(); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			c.deleteContainer(id)
		}
	}()

	// uid/gid
	rootfsDir := filepath.Join(container.dir, "rootfs")
	if err := idtools.MkdirAllAs(rootfsDir, 0700, uid, gid); err != nil && !os.IsExist(err) {
		return err
	}
	if err := syscall.Mount(spec.Root.Path, rootfsDir, "bind", syscall.MS_REC|syscall.MS_BIND, ""); err != nil {
		return err
	}
	spec.Root.Path = "rootfs"

	f, err := os.Create(filepath.Join(container.dir, configFilename))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(spec); err != nil {
		return err
	}

	return container.start()
}
