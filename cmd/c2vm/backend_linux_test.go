//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirecrackerBackendFlags(t *testing.T) {
	// Without -firecracker-binary, firecracker is looked up on PATH.
	t.Setenv("PATH", t.TempDir())
	opts, err := parseBoot([]string{"-kernel", "vmlinux", "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.backend(); err == nil || !strings.Contains(err.Error(), "firecracker not found on PATH") {
		t.Errorf("backend() without firecracker on PATH = %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "firecracker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := opts.backend(); err != nil {
		t.Errorf("backend() with firecracker on PATH: %v", err)
	}

	opts, err = parseBoot([]string{"-kernel", "vmlinux", "-firecracker-binary", "/usr/bin/firecracker", "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.backend(); err != nil {
		t.Errorf("backend(): %v", err)
	}
}
