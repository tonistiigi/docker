package libcontainerd

import "github.com/opencontainers/specs"

func getRootIDs(s specs.LinuxSpec) (int, int, error) {
	var hasUserns bool
	for _, ns := range s.Linux.Namespaces {
		if ns.Type == specs.UserNamespace {
			hasUserns = true
			break
		}
	}
	if !hasUserns {
		return 0, 0, nil
	}
	uid := hostIDFromMap(0, s.Linux.UIDMappings)
	gid := hostIDFromMap(0, s.Linux.GIDMappings)
	return uid, gid, nil
}

func hostIDFromMap(id uint32, mp []specs.IDMapping) int {
	for _, m := range mp {
		if id >= m.ContainerID && id <= m.ContainerID+m.Size-1 {
			return int(m.HostID + id - m.ContainerID)
		}
	}
	return 0
}
