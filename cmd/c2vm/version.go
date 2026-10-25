package main

import (
	"bytes"
	"debug/buildinfo"
	"fmt"
	"runtime/debug"
)

// version returns c2vm's version. The go command stamps it from git: the
// tag when built from a tagged commit, a pseudo-version otherwise, with
// +dirty when the tree has changes.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return moduleVersion(bi)
}

func moduleVersion(bi *debug.BuildInfo) string {
	// (devel) is what binaries built outside of git, or by go run, get.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "devel"
}

// initVersion describes the c2vm-init built into c2vm, from the build
// information the go command stamps into it as into c2vm. make build
// builds both from the same tree, so they have the same version.
func initVersion(init []byte) string {
	if len(init) == 0 {
		return "c2vm-init: not built in (build c2vm with make, or boot with -init)"
	}
	bi, err := buildinfo.Read(bytes.NewReader(init))
	if err != nil {
		return fmt.Sprintf("c2vm-init: built in, version unknown (%v)", err)
	}
	var goos, goarch string
	for _, s := range bi.Settings {
		switch s.Key {
		case "GOOS":
			goos = s.Value
		case "GOARCH":
			goarch = s.Value
		}
	}
	return fmt.Sprintf("c2vm-init %s (built in, %s/%s)", moduleVersion(bi), goos, goarch)
}
