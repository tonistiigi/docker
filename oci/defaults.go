package oci

import (
	"os"
	"runtime"

	"github.com/opencontainers/specs"
)

func sPtr(s string) *string      { return &s }
func rPtr(r rune) *rune          { return &r }
func iPtr(i int64) *int64        { return &i }
func u32Ptr(i int64) *uint32     { u := uint32(i); return &u }
func fmPtr(i int64) *os.FileMode { fm := os.FileMode(i); return &fm }

var defaultTemplate = specs.Spec{
	Version: specs.Version,
	Platform: specs.Platform{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	},
}
