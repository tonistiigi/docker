package oci

import "github.com/opencontainers/specs"

// DefaultSpec returns default oci spec used by docker.
func DefaultSpec() specs.LinuxSpec {
	s := defaultTemplate
	s.Mounts = []specs.Mount{
		{
			Destination: "/proc",
			Type:        "proc",
			Source:      "proc",
			Options:     []string{"nosuid", "noexec", "nodev"},
		},
		{
			Destination: "/dev",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "strictatime", "mode=755"},
		},
		{
			Destination: "/dev/pts",
			Type:        "devpts",
			Source:      "devpts",
			Options:     []string{"newinstance", "ptmxmode=0666", "mode=0620", "gid=5"},
		},
		{
			Destination: "/sys",
			Type:        "sysfs",
			Source:      "sysfs",
			Options:     []string{"nosuid", "noexec", "nodev", "ro"},
		},
		{
			Destination: "/sys/fs/cgroup",
			Type:        "cgroup",
			Source:      "cgroup",
			Options:     []string{"nosuid", "noexec", "nodev"},
		},
		{
			Destination: "/dev/mqueue",
			Type:        "mqueue",
			Source:      "mqueue",
			Options:     []string{"nosuid", "noexec", "nodev"},
		},
	}

	ls := specs.Linux{
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
		Namespaces: []specs.Namespace{
			{Type: "mount"},
			{Type: "network"},
			{Type: "uts"},
			{Type: "pid"},
			{Type: "ipc"},
		},
		Devices: []specs.Device{
			{
				Type:     'c',
				Path:     "/dev/zero",
				Major:    1,
				Minor:    5,
				FileMode: fmPtr(0666),
				UID:      u32Ptr(0),
				GID:      u32Ptr(0),
			},
			{
				Type:     'c',
				Path:     "/dev/null",
				Major:    1,
				Minor:    3,
				FileMode: fmPtr(0666),
				UID:      u32Ptr(0),
				GID:      u32Ptr(0),
			},
			{
				Type:     'c',
				Path:     "/dev/urandom",
				Major:    1,
				Minor:    9,
				FileMode: fmPtr(0666),
				UID:      u32Ptr(0),
				GID:      u32Ptr(0),
			},
			{
				Type:     'c',
				Path:     "/dev/random",
				Major:    1,
				Minor:    8,
				FileMode: fmPtr(0666),
				UID:      u32Ptr(0),
				GID:      u32Ptr(0),
			},
			// {
			// 	Type:     'c',
			// 	Path:     "/dev/tty",
			// 	Major:    5,
			// 	Minor:    0,
			// 	FileMode: fmPtr(0666),
			// 	UID:      u32Ptr(0),
			// 	GID:      u32Ptr(0),
			// },
			// {
			// 	Type:     'c',
			// 	Path:     "/dev/console",
			// 	Major:    5,
			// 	Minor:    1,
			// 	FileMode: fmPtr(0666),
			// 	UID:      u32Ptr(0),
			// 	GID:      u32Ptr(0),
			// },
			{
				Type:     'c',
				Path:     "/dev/fuse",
				Major:    10,
				Minor:    229,
				FileMode: fmPtr(0666),
				UID:      u32Ptr(0),
				GID:      u32Ptr(0),
			},
		},
		Resources: &specs.Resources{
			Devices: []specs.DeviceCgroup{
				{
					Allow:  false,
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(1),
					Minor:  iPtr(5),
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(1),
					Minor:  iPtr(3),
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(1),
					Minor:  iPtr(9),
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(1),
					Minor:  iPtr(8),
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(5),
					Minor:  iPtr(0),
					Access: sPtr("rwm"),
				},
				{
					Allow:  true,
					Type:   rPtr('c'),
					Major:  iPtr(5),
					Minor:  iPtr(1),
					Access: sPtr("rwm"),
				},
				{
					Allow:  false,
					Type:   rPtr('c'),
					Major:  iPtr(10),
					Minor:  iPtr(229),
					Access: sPtr("rwm"),
				},
			},
		},
	}

	return specs.LinuxSpec{
		Spec:  s,
		Linux: ls,
	}
}
