package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/caps"
	"github.com/docker/docker/pkg/idtools"
	"github.com/opencontainers/runc/libcontainer/devices"
	"github.com/opencontainers/runc/libcontainer/user"
	"github.com/opencontainers/specs"
)

type combinedLinuxSpec struct {
	spec  specs.Linux
	rspec specs.LinuxRuntime
}

func setResources(s *specs.LinuxRuntimeSpec, c *container.Container) error {
	weightDevices, err := getBlkioWeightDevices(c.HostConfig)
	if err != nil {
		return err
	}

	readBpsDevice, err := getBlkioReadBpsDevices(c.HostConfig)
	if err != nil {
		return err
	}

	writeBpsDevice, err := getBlkioWriteBpsDevices(c.HostConfig)
	if err != nil {
		return err
	}

	readIOpsDevice, err := getBlkioReadIOpsDevices(c.HostConfig)
	if err != nil {
		return err
	}

	writeIOpsDevice, err := getBlkioWriteIOpsDevices(c.HostConfig)
	if err != nil {
		return err
	}

	r := &specs.Resources{
		Memory: specs.Memory{
			Limit:       uint64(c.HostConfig.Memory),
			Reservation: uint64(c.HostConfig.MemoryReservation),
			Swap:        uint64(c.HostConfig.MemorySwap),
			Kernel:      uint64(c.HostConfig.KernelMemory),
		},
		CPU: specs.CPU{
			Shares: uint64(c.HostConfig.CPUShares),
			Cpus:   c.HostConfig.CpusetCpus,
			Mems:   c.HostConfig.CpusetMems,
			Period: uint64(c.HostConfig.CPUPeriod),
			Quota:  uint64(c.HostConfig.CPUQuota),
		},
		BlockIO: specs.BlockIO{
			Weight:                  c.HostConfig.BlkioWeight,
			WeightDevice:            weightDevices,
			ThrottleReadBpsDevice:   readBpsDevice,
			ThrottleWriteBpsDevice:  writeBpsDevice,
			ThrottleReadIOPSDevice:  readIOpsDevice,
			ThrottleWriteIOPSDevice: writeIOpsDevice,
		},
	}
	if c.HostConfig.OomKillDisable != nil {
		r.DisableOOMKiller = *c.HostConfig.OomKillDisable
	}

	if sw := c.HostConfig.MemorySwappiness; sw != nil {
		swappiness := *sw
		r.Memory.Swappiness = uint64(swappiness)
		if swappiness == -1 {
			r.Memory.Swappiness = 0
		}
	}
	s.Linux.Resources = r
	return nil
}

func setDevices(s *specs.LinuxRuntimeSpec, c *container.Container) error {
	// Build lists of devices allowed and created within the container.
	var devs []specs.Device
	if c.HostConfig.Privileged {
		hostDevices, err := devices.HostDevices()
		if err != nil {
			return err
		}
		for _, d := range hostDevices {
			devs = append(devs, specDevice(d))
		}
	} else {
		// FIXME: This is missing the cgroups allowed devices conf + fuse
		for _, deviceMapping := range c.HostConfig.Devices {
			d, err := getDevicesFromPath(deviceMapping)
			if err != nil {
				return err
			}

			devs = append(devs, d...)
		}
	}

	s.Linux.Devices = append(s.Linux.Devices, devs...)

	return nil
}

func setRlimits(s *specs.LinuxRuntimeSpec, c *container.Container) error {
	var rlimits []specs.Rlimit

	// Merge ulimits with daemon defaults
	ulIdx := make(map[string]struct{})
	for _, ul := range c.HostConfig.Ulimits {
		if _, exists := ulIdx[ul.Name]; !exists {
			rlimits = append(rlimits, specs.Rlimit{
				Type: "RLIMIT_" + strings.ToUpper(ul.Name),
				Soft: uint64(ul.Soft),
				Hard: uint64(ul.Hard),
			})
			ulIdx[ul.Name] = struct{}{}
		}
	}

	s.Linux.Rlimits = rlimits
	return nil
}

func setUser(s *specs.LinuxSpec, c *container.Container) error {
	passwdPath, err := user.GetPasswdPath()
	if err != nil {
		return err
	}
	groupPath, err := user.GetGroupPath()
	if err != nil {
		return err
	}
	execUser, err := user.GetExecUserPath(c.Config.User, nil, passwdPath, groupPath)
	if err != nil {
		return err
	}

	var addGroups []int
	if len(c.HostConfig.GroupAdd) > 0 {
		addGroups, err = user.GetAdditionalGroupsPath(c.HostConfig.GroupAdd, groupPath)
		if err != nil {
			return err
		}
	}
	s.Process.User.UID = uint32(execUser.Uid)
	s.Process.User.GID = uint32(execUser.Gid)
	sgids := append(execUser.Sgids, addGroups...)
	for _, g := range sgids {
		s.Process.User.AdditionalGids = append(s.Process.User.AdditionalGids, uint32(g))
	}
	return nil
}

func setNamespace(s *specs.LinuxRuntimeSpec, ns specs.Namespace) {
	for i, n := range s.Linux.Namespaces {
		if n.Type == ns.Type {
			s.Linux.Namespaces[i] = ns
			return
		}
	}
	s.Linux.Namespaces = append(s.Linux.Namespaces, ns)
}

func setCapabilities(s *specs.LinuxSpec, c *container.Container) error {
	var caplist []string
	var err error
	if c.HostConfig.Privileged {
		caplist = caps.GetAllCapabilities()
	} else {
		caplist, err = caps.TweakCapabilities(s.Linux.Capabilities, c.HostConfig.CapAdd.Slice(), c.HostConfig.CapDrop.Slice())
		if err != nil {
			return err
		}
	}
	s.Linux.Capabilities = caplist
	return nil
}

func delNamespace(s *specs.LinuxRuntimeSpec, nsType specs.NamespaceType) {
	idx := -1
	for i, n := range s.Linux.Namespaces {
		if n.Type == nsType {
			idx = i
		}
	}
	if idx >= 0 {
		s.Linux.Namespaces = append(s.Linux.Namespaces[:idx], s.Linux.Namespaces[idx+1:]...)
	}
}

func setNamespaces(daemon *Daemon, s *specs.LinuxRuntimeSpec, c *container.Container) error {
	// network
	if !c.Config.NetworkDisabled {
		ns := specs.Namespace{Type: "network"}
		parts := strings.SplitN(string(c.HostConfig.NetworkMode), ":", 2)
		if parts[0] == "container" {
			nc, err := daemon.getNetworkedContainer(c.ID, c.HostConfig.NetworkMode.ConnectedContainer())
			if err != nil {
				return err
			}
			ns.Path = fmt.Sprintf("/proc/%d/ns/net", nc.State.GetPID())
		} else if c.HostConfig.NetworkMode.IsHost() {
			ns.Path = c.NetworkSettings.SandboxKey
		}
		setNamespace(s, ns)
	}
	// ipc
	if c.HostConfig.IpcMode.IsContainer() {
		ns := specs.Namespace{Type: "ipc"}
		ic, err := daemon.getIpcContainer(c)
		if err != nil {
			return err
		}
		ns.Path = fmt.Sprintf("/proc/%d/ns/net", ic.State.GetPID())
		setNamespace(s, ns)
	} else {
		delNamespace(s, specs.NamespaceType("ipc"))
	}
	// pid
	if c.HostConfig.PidMode.IsHost() {
		delNamespace(s, specs.NamespaceType("pid"))
	}
	// uts
	if c.HostConfig.UTSMode.IsHost() {
		delNamespace(s, specs.NamespaceType("uts"))
	}
	// user
	uidMap, gidMap := daemon.GetUIDGIDMaps()
	if uidMap != nil {
		ns := specs.Namespace{Type: "user"}
		setNamespace(s, ns)
		s.Linux.UIDMappings = specMapping(uidMap)
		s.Linux.GIDMappings = specMapping(gidMap)
	}

	return nil
}

func specMapping(s []idtools.IDMap) []specs.IDMapping {
	var ids []specs.IDMapping
	for _, item := range s {
		ids = append(ids, specs.IDMapping{
			HostID:      uint32(item.HostID),
			ContainerID: uint32(item.ContainerID),
			Size:        uint32(item.Size),
		})
	}
	return ids
}

func execToSpecMount(m container.Mount) specs.Mount {
	opts := []string{"rbind"}
	// FIXME: mount propagation parsing/validation: #17034
	if !m.Writable {
		opts = append(opts, "ro")
	}
	return specs.Mount{
		Type:    "bind",
		Source:  m.Source,
		Options: append(opts, m.Data),
	}
}

func setMounts(daemon *Daemon, s *specs.LinuxSpec, rs *specs.LinuxRuntimeSpec, c *container.Container, mounts []container.Mount) error {
	userMounts := make(map[string]struct{})
	for _, m := range mounts {
		userMounts[m.Destination] = struct{}{}
	}

	// Filter out mounts that are overriden by user supplied mounts
	var defaultMPs []specs.MountPoint
	_, mountDev := userMounts["/dev"]
	for _, mp := range s.Mounts {
		if _, ok := userMounts[mp.Path]; !ok {
			if mountDev && strings.HasPrefix(mp.Path, "/dev/") {
				continue
			}
			defaultMPs = append(defaultMPs, mp)
		}
	}
	s.Mounts = defaultMPs
	for _, m := range mounts {
		s.Mounts = append(s.Mounts, specs.MountPoint{Name: m.Destination, Path: m.Destination})
		rs.Mounts[m.Destination] = execToSpecMount(m)
	}
	return nil
}

func (daemon *Daemon) writeBundle(s combinedSpec, c *container.Container, mounts []container.Mount) error {
	ls := specs.LinuxSpec{
		Spec:  s.spec,
		Linux: defaultLinuxTemplate.spec,
	}
	rls := specs.LinuxRuntimeSpec{
		RuntimeSpec: s.rspec,
		Linux:       defaultLinuxTemplate.rspec,
	}
	if err := setResources(&rls, c); err != nil {
		return fmt.Errorf("linux runtime spec resources: %v")
	}
	if err := setDevices(&rls, c); err != nil {
		return fmt.Errorf("linux runtime spec devices: %v")
	}
	if err := setRlimits(&rls, c); err != nil {
		return fmt.Errorf("linux runtime spec rlimits: %v")
	}
	if err := setUser(&ls, c); err != nil {
		return fmt.Errorf("linux spec user: %v")
	}
	if err := setNamespaces(daemon, &rls, c); err != nil {
		return fmt.Errorf("linux spec namespaces: %v", err)
	}
	if err := setCapabilities(&ls, c); err != nil {
		return fmt.Errorf("linux spec capabilities: %v", err)
	}
	if err := setMounts(daemon, &ls, &rls, c, mounts); err != nil {
		return fmt.Errorf("linux mounts: %v", err)
	}

	for _, ns := range rls.Linux.Namespaces {
		if ns.Type == "network" && ns.Path == "" {
			target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "exe"))
			if err != nil {
				return err
			}

			rls.RuntimeSpec.Hooks = specs.Hooks{
				Prestart: []specs.Hook{{
					Path: target, // FIXME: cross-platform
					Args: []string{"libnetwork-setkey", c.ID, daemon.netController.ID()},
				}},
			}
		}
	}

	rootUID, rootGID := daemon.GetRemappedUIDGID()
	bundleDir := filepath.Join(daemon.root, "bundles", c.ID)
	rootfsDir := filepath.Join(bundleDir, "rootfs")
	if err := idtools.MkdirAllAs(rootfsDir, 0700, rootUID, rootGID); err != nil && !os.IsExist(err) {
		return err
	}
	if err := syscall.Mount(c.BaseFS, rootfsDir, "bind", syscall.MS_REC|syscall.MS_BIND, ""); err != nil {
		return err
	}
	cPath := filepath.Join(bundleDir, "config.json")
	cf, err := os.Create(cPath)
	if err != nil {
		return err
	}
	logrus.Debugf("Write oci to: %s", cPath)
	if err := json.NewEncoder(cf).Encode(ls); err != nil {
		return err
	}
	rPath := filepath.Join(bundleDir, "runtime.json")
	rf, err := os.Create(rPath)
	if err != nil {
		return err
	}
	logrus.Debugf("Write oci to: %s", rPath)
	if err := json.NewEncoder(rf).Encode(rls); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) destroyBundle(id string) {
	bundleDir := filepath.Join(daemon.root, "bundles", id)
	syscall.Unmount(filepath.Join(bundleDir, "rootfs"), syscall.MNT_DETACH)
	os.RemoveAll(bundleDir)
}
