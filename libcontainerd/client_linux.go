package libcontainerd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/pkg/idtools"
	"github.com/opencontainers/specs"
	"golang.org/x/net/context"
)

func (c *client) AddProcess(id, processID string, specp Process) error {
	c.lock(id)
	defer c.unlock(id)
	container, err := c.getContainer(id)
	if err != nil {
		return err
	}

	var spec specs.Spec
	dt, err := ioutil.ReadFile(filepath.Join(container.dir, configFilename))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(dt, &spec); err != nil {
		return err
	}

	sp := spec.Process
	sp.Args = specp.Args
	sp.Terminal = specp.Terminal
	if specp.Env != nil {
		sp.Env = specp.Env
	}
	if specp.Cwd != nil {
		sp.Cwd = *specp.Cwd
	}
	if specp.User != nil {
		sp.User = specs.User{
			UID:            specp.User.UID,
			GID:            specp.User.GID,
			AdditionalGids: specp.User.AdditionalGids,
		}
	}

	p := container.newProcess(processID)

	r := &containerd.AddProcessRequest{
		Args:     sp.Args,
		Cwd:      sp.Cwd,
		Terminal: sp.Terminal,
		Id:       id,
		Env:      sp.Env,
		User: &containerd.User{
			Uid:            sp.User.UID,
			Gid:            sp.User.GID,
			AdditionalGids: sp.User.AdditionalGids,
		},
		Pid:    processID,
		Stdin:  p.fifo(syscall.Stdin),
		Stdout: p.fifo(syscall.Stdout),
		Stderr: p.fifo(syscall.Stderr),
	}

	iopipe, err := p.openFifos()
	if err != nil {
		return err
	}

	if _, err := c.remote.apiClient.AddProcess(context.Background(), r); err != nil {
		return err
	}

	container.processes[processID] = p

	c.unlock(id)

	if err := c.backend.AttachStreams(processID, *iopipe); err != nil {
		return err
	}
	c.lock(id)

	return nil
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

func (c *client) Create(id string, spec Spec, options ...CreateOption) (err error) {
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

	uid, gid, err := getRootIDs(specs.LinuxSpec(spec))
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
