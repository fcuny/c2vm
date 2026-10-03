package main

import (
	"runtime"
	"strings"
	"testing"
)

var required = []string{"-container", "docker.io/library/alpine:latest", "-kernel", "vmlinux", "-firecracker-binary", "firecracker"}

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(required)
	if err != nil {
		t.Fatal(err)
	}
	if opts.platform.OS != "linux" || opts.platform.Architecture != runtime.GOARCH {
		t.Errorf("platform = %+v, want linux/%s", opts.platform, runtime.GOARCH)
	}
	if opts.cpus != 1 || opts.memoryMiB != 512 {
		t.Errorf("unexpected defaults: %+v", opts)
	}
	if opts.socketPath != "" {
		t.Errorf("socket path defaults to %q, want a temporary one", opts.socketPath)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-kernel", "k", "-firecracker-binary", "f"}, "a container is required"},
		{[]string{"-container", "c", "-firecracker-binary", "f"}, "a linux kernel is required"},
		{[]string{"-container", "c", "-kernel", "k"}, "firecracker binary is required"},
		{append([]string{"-cpus", "0"}, required...), "-cpus"},
		{append([]string{"-memory", "0"}, required...), "-memory"},
		{append([]string{"-platform", "windows/amd64"}, required...), "only linux"},
		{append([]string{"-platform", "linux/not-an-arch/x/y"}, required...), "invalid -platform"},
	} {
		_, err := parseFlags(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseFlags(%q) = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}
