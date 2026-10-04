package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseSave(t *testing.T) {
	opts, err := parseSave([]string{"nginx:stable", "-o", "nginx.ext4", "-cache-dir", "/tmp/c2vm"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.image != "nginx:stable" || opts.out != "nginx.ext4" || opts.cacheDir != "/tmp/c2vm" {
		t.Errorf("unexpected options: %+v", opts)
	}
	if opts.platform.OS != "linux" || opts.platform.Architecture != runtime.GOARCH {
		t.Errorf("platform = %+v, want linux/%s", opts.platform, runtime.GOARCH)
	}
}

func TestParseSaveDefaultCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	t.Setenv("HOME", "/home/user")
	opts, err := parseSave([]string{"alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(opts.cacheDir, "/c2vm") {
		t.Errorf("cache dir = %q, want a c2vm directory in the user's cache", opts.cacheDir)
	}
}

func TestParseBoot(t *testing.T) {
	// Flags can come before or after the image.
	opts, err := parseBoot([]string{"-kernel", "vmlinux", "alpine:3.24", "-cpus", "2", "-memory", "1024"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.image != "alpine:3.24" || opts.kernel != "vmlinux" || opts.cpus != 2 || opts.memoryMiB != 1024 {
		t.Errorf("unexpected options: %+v", opts)
	}
}

func TestParseBootCommand(t *testing.T) {
	// Everything after -- is the command, flags included.
	opts, err := parseBoot([]string{"-cpus", "2", "alpine", "--", "ls", "-l", "--", "/"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.image != "alpine" || opts.cpus != 2 {
		t.Errorf("unexpected options: %+v", opts)
	}
	if got, want := strings.Join(opts.command, " "), "ls -l -- /"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}

	opts, err = parseBoot([]string{"alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != nil {
		t.Errorf("command = %q, want none", opts.command)
	}
}

func TestParseBootDefaultKernel(t *testing.T) {
	opts, err := parseBoot([]string{"alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.kernel != defaultKernel {
		t.Errorf("kernel = %q, want %q", opts.kernel, defaultKernel)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		parse func([]string) error
		args  []string
		want  string
	}{
		{saveErr, nil, "an image is required"},
		{saveErr, []string{"alpine", "nginx"}, "expected one image"},
		{saveErr, []string{"-platform", "windows/amd64", "alpine"}, "only linux"},
		{saveErr, []string{"-platform", "linux/not-an-arch/x/y", "alpine"}, "invalid -platform"},
		{bootErr, []string{"-kernel", "k", "-cpus", "0", "alpine"}, "-cpus"},
		{bootErr, []string{"-kernel", "k", "-memory", "0", "alpine"}, "-memory"},
		{bootErr, []string{"alpine", "--"}, "expected a command"},
		{bootErr, []string{"--", "echo", "hi"}, "an image is required"},
		{bootErr, []string{"alpine", "nginx", "--", "echo"}, "expected one image"},
	} {
		err := tc.parse(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parsing %q = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
}

func saveErr(args []string) error { _, err := parseSave(args); return err }
func bootErr(args []string) error { _, err := parseBoot(args); return err }
