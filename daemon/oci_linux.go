package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/caps"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/mount"
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

	memoryRes := getMemoryResources(c.HostConfig)

	cpuRes := getCPUResources(c.HostConfig)

	blkioWeight := c.HostConfig.BlkioWeight

	r := &specs.Resources{
		Memory: memoryRes,
		CPU:    cpuRes,
		BlockIO: &specs.BlockIO{
			Weight:                  &blkioWeight,
			WeightDevice:            weightDevices,
			ThrottleReadBpsDevice:   readBpsDevice,
			ThrottleWriteBpsDevice:  writeBpsDevice,
			ThrottleReadIOPSDevice:  readIOpsDevice,
			ThrottleWriteIOPSDevice: writeIOpsDevice,
		},
		DisableOOMKiller: c.HostConfig.OomKillDisable,
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
	uid, gid, additionalGids, err := getUser(c)
	if err != nil {
		return err
	}
	s.Process.User.UID = uid
	s.Process.User.GID = gid
	s.Process.User.AdditionalGids = additionalGids
	return nil
}

func getUser(c *container.Container) (uint32, uint32, []uint32, error) {
	passwdPath, err := user.GetPasswdPath()
	if err != nil {
		return 0, 0, nil, err
	}
	groupPath, err := user.GetGroupPath()
	if err != nil {
		return 0, 0, nil, err
	}
	execUser, err := user.GetExecUserPath(c.Config.User, nil, passwdPath, groupPath)
	if err != nil {
		return 0, 0, nil, err
	}

	var addGroups []int
	if len(c.HostConfig.GroupAdd) > 0 {
		addGroups, err = user.GetAdditionalGroupsPath(c.HostConfig.GroupAdd, groupPath)
		if err != nil {
			return 0, 0, nil, err
		}
	}
	uid := uint32(execUser.Uid)
	gid := uint32(execUser.Gid)
	sgids := append(execUser.Sgids, addGroups...)
	var additionalGids []uint32
	for _, g := range sgids {
		additionalGids = append(additionalGids, uint32(g))
	}
	return uid, gid, additionalGids, nil
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
		ns.Path = fmt.Sprintf("/proc/%d/ns/ipc", ic.State.GetPID())
		setNamespace(s, ns)
	} else if c.HostConfig.IpcMode.IsHost() {
		delNamespace(s, specs.NamespaceType("ipc"))
	} else {
		ns := specs.Namespace{Type: "ipc"}
		setNamespace(s, ns)
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

func getMountInfo(mountinfo []*mount.Info, dir string) *mount.Info {
	for _, m := range mountinfo {
		if m.Mountpoint == dir {
			return m
		}
	}
	return nil
}

// Get the source mount point of directory passed in as argument. Also return
// optional fields.
func getSourceMount(source string) (string, string, error) {
	// Ensure any symlinks are resolved.
	sourcePath, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", err
	}

	mountinfos, err := mount.GetMounts()
	if err != nil {
		return "", "", err
	}

	mountinfo := getMountInfo(mountinfos, sourcePath)
	if mountinfo != nil {
		return sourcePath, mountinfo.Optional, nil
	}

	path := sourcePath
	for {
		path = filepath.Dir(path)

		mountinfo = getMountInfo(mountinfos, path)
		if mountinfo != nil {
			return path, mountinfo.Optional, nil
		}

		if path == "/" {
			break
		}
	}

	// If we are here, we did not find parent mount. Something is wrong.
	return "", "", fmt.Errorf("Could not find source mount of %s", source)
}

// Ensure mount point on which path is mounted, is shared.
func ensureShared(path string) error {
	sharedMount := false

	sourceMount, optionalOpts, err := getSourceMount(path)
	if err != nil {
		return err
	}
	// Make sure source mount point is shared.
	optsSplit := strings.Split(optionalOpts, " ")
	for _, opt := range optsSplit {
		if strings.HasPrefix(opt, "shared:") {
			sharedMount = true
			break
		}
	}

	if !sharedMount {
		return fmt.Errorf("Path %s is mounted on %s but it is not a shared mount.", path, sourceMount)
	}
	return nil
}

// Ensure mount point on which path is mounted, is either shared or slave.
func ensureSharedOrSlave(path string) error {
	sharedMount := false
	slaveMount := false

	sourceMount, optionalOpts, err := getSourceMount(path)
	if err != nil {
		return err
	}
	// Make sure source mount point is shared.
	optsSplit := strings.Split(optionalOpts, " ")
	for _, opt := range optsSplit {
		if strings.HasPrefix(opt, "shared:") {
			sharedMount = true
			break
		} else if strings.HasPrefix(opt, "master:") {
			slaveMount = true
			break
		}
	}

	if !sharedMount && !slaveMount {
		return fmt.Errorf("Path %s is mounted on %s but it is not a shared or slave mount.", path, sourceMount)
	}
	return nil
}

var (
	mountPropagationMap = map[string]int{
		"private":  mount.PRIVATE,
		"rprivate": mount.RPRIVATE,
		"shared":   mount.SHARED,
		"rshared":  mount.RSHARED,
		"slave":    mount.SLAVE,
		"rslave":   mount.RSLAVE,
	}

	mountPropagationReverseMap = map[int]string{
		mount.PRIVATE:  "private",
		mount.RPRIVATE: "rprivate",
		mount.SHARED:   "shared",
		mount.RSHARED:  "rshared",
		mount.SLAVE:    "slave",
		mount.RSLAVE:   "rslave",
	}
)

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

		// Determine property of RootPropagation based on volume
		// properties. If a volume is shared, then keep root propagation
		// shared. This should work for slave and private volumes too.
		//
		// For slave volumes, it can be either [r]shared/[r]slave.
		//
		// For private volumes any root propagation value should work.
		pFlag := mountPropagationMap[m.Propagation]
		if pFlag == mount.SHARED || pFlag == mount.RSHARED {
			if err := ensureShared(m.Source); err != nil {
				return err
			}
			rootpg := mountPropagationMap[rs.Linux.RootfsPropagation]
			if rootpg != mount.SHARED && rootpg != mount.RSHARED {
				rs.Linux.RootfsPropagation = mountPropagationReverseMap[mount.SHARED]
			}
		} else if pFlag == mount.SLAVE || pFlag == mount.RSLAVE {
			if err := ensureSharedOrSlave(m.Source); err != nil {
				return err
			}
			rootpg := mountPropagationMap[rs.Linux.RootfsPropagation]
			if rootpg != mount.SHARED && rootpg != mount.RSHARED && rootpg != mount.SLAVE && rootpg != mount.RSLAVE {
				rs.Linux.RootfsPropagation = mountPropagationReverseMap[mount.RSLAVE]
			}
		}

		specMount := execToSpecMount(m)
		if pFlag != 0 {
			specMount.Options = append(specMount.Options, mountPropagationReverseMap[pFlag])
		}

		rs.Mounts[m.Destination] = specMount

		fmt.Printf("specMount: %#v\n", specMount)
	}
	fmt.Printf("rootfsPropagation: %#v\n", rs.Linux.RootfsPropagation)
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
		return fmt.Errorf("linux runtime spec resources: %v", err)
	}
	if err := setDevices(&rls, c); err != nil {
		return fmt.Errorf("linux runtime spec devices: %v", err)
	}
	if err := setRlimits(&rls, c); err != nil {
		return fmt.Errorf("linux runtime spec rlimits: %v", err)
	}
	if err := setUser(&ls, c); err != nil {
		return fmt.Errorf("linux spec user: %v", err)
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
	if err := setSeccomp(daemon, &rls, c); err != nil {
		return fmt.Errorf("linux seccomp: %v", err)
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
	if err := json.NewEncoder(cf).Encode(ls); err != nil {
		return err
	}
	rPath := filepath.Join(bundleDir, "runtime.json")
	rf, err := os.Create(rPath)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(rf).Encode(rls); err != nil {
		return err
	}

	return nil
}

func (daemon *Daemon) destroyBundle(id string) {
	// bundleDir := filepath.Join(daemon.root, "bundles", id)
	// syscall.Unmount(filepath.Join(bundleDir, "rootfs"), syscall.MNT_DETACH)
	// os.RemoveAll(bundleDir)
}
