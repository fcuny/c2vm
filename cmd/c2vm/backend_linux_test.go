//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestFirecrackerBackendFlags(t *testing.T) {
	opts, err := parseBoot([]string{"-kernel", "vmlinux", "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.backend(); err == nil || !strings.Contains(err.Error(), "firecracker binary is required") {
		t.Errorf("backend() without -firecracker-binary = %v", err)
	}

	opts, err = parseBoot([]string{"-kernel", "vmlinux", "-firecracker-binary", "/usr/bin/firecracker", "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.backend(); err != nil {
		t.Errorf("backend(): %v", err)
	}
}
