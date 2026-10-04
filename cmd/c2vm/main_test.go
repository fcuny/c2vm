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

func TestParseBootEnv(t *testing.T) {
	t.Setenv("C2VM_TEST_SET", "from host")
	opts, err := parseBoot([]string{"-e", "A=1", "alpine", "-e", "B=x=y", "-e", "C2VM_TEST_SET", "-e", "C2VM_TEST_UNSET", "-e", "EMPTY="})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A=1", "B=x=y", "C2VM_TEST_SET=from host", "EMPTY="}
	if strings.Join(opts.env, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q, want %q", opts.env, want)
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
		{bootErr, []string{"-e", "=1", "alpine"}, "the name is empty"},
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
