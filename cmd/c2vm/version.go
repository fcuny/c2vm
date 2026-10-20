package main

import "runtime/debug"

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
