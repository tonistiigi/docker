package daemon

import "github.com/opencontainers/specs"

var defaultTemplate = combinedSpec{
	spec: specs.Spec{
		Mounts: []specs.MountPoint{
			{
				Name: "proc",
				Path: "/proc",
			},
			{
				Name: "dev",
				Path: "/dev",
			},
			{
				Name: "devpts",
				Path: "/dev/pts",
			},
			{
				Name: "shm",
				Path: "/dev/shm",
			},
			{
				Name: "mqueue",
				Path: "/dev/mqueue",
			},
			{
				Name: "sysfs",
				Path: "/sys",
			},
			{
				Name: "cgroup",
				Path: "/sys/fs/cgroup",
			},
		},
	},
	rspec: specs.RuntimeSpec{
		Mounts: map[string]specs.Mount{
			"proc": {
				Type:    "proc",
				Source:  "proc",
				Options: []string{"nosuid", "noexec", "nodev"},
			},
			"dev": {
				Type:    "tmpfs",
				Source:  "tmpfs",
				Options: []string{"nosuid", "strictatime", "mode=755"},
			},
			"devpts": {
				Type:    "devpts",
				Source:  "devpts",
				Options: []string{"newinstance", "ptmxmode=0666", "mode=0620", "gid=5"},
			},
			"shm": {
				Type:    "tmpfs",
				Source:  "shm",
				Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
			},
			"mqueue": {
				Type:    "mqueue",
				Source:  "mqueue",
				Options: []string{"nosuid", "noexec", "nodev"},
			},
			"sysfs": {
				Type:    "sysfs",
				Source:  "sysfs",
				Options: []string{"nosuid", "noexec", "nodev", "ro"},
			},
			"cgroup": {
				Type:    "cgroup",
				Source:  "cgroup",
				Options: []string{"nosuid", "noexec", "nodev", "ro"},
			},
		},
	},
}

var defaultLinuxTemplate = combinedLinuxSpec{
	spec: specs.Linux{
		Capabilities: []string{
			"CAP_CHOWN",
			"CAP_DAC_OVERRIDE",
			"CAP_FSETID",
			"CAP_FOWNER",
			"CAP_MKNOD",
			"CAP_NET_RAW",
			"CAP_SETGID",
			"CAP_SETUID",
			"CAP_SETFCAP",
			"CAP_SETPCAP",
			"CAP_NET_BIND_SERVICE",
			"CAP_SYS_CHROOT",
			"CAP_KILL",
			"CAP_AUDIT_WRITE",
		},
	},
	rspec: specs.LinuxRuntime{
		Namespaces: []specs.Namespace{
			{Type: "mount"},
			{Type: "network"},
			{Type: "uts"},
			{Type: "pid"},
			{Type: "ipc"},
		},
		Devices: []specs.Device{
			{
				Type:        'c',
				Path:        "/dev/zero",
				Major:       1,
				Minor:       5,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
			{
				Type:        'c',
				Path:        "/dev/null",
				Major:       1,
				Minor:       3,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
			{
				Type:        'c',
				Path:        "/dev/urandom",
				Major:       1,
				Minor:       9,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
			{
				Type:        'c',
				Path:        "/dev/random",
				Major:       1,
				Minor:       8,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
			{
				Type:        'c',
				Path:        "/dev/tty",
				Major:       5,
				Minor:       0,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
			{
				Type:        'c',
				Path:        "/dev/console",
				Major:       5,
				Minor:       1,
				Permissions: "rwm",
				FileMode:    0666,
				UID:         0,
				GID:         0,
			},
		},
	},
}
