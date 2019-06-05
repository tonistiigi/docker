package binfmt

import "os"

func IsEmulatedQEMUUser() bool {
	f, err := os.Open("/proc/self/is_qemu")
	if err != nil {
		return false
	}
	f.Close()
	return true
}
