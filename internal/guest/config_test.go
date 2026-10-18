package guest

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestFromImage(t *testing.T) {
	c, err := FromImage(ocispec.ImageConfig{
		Entrypoint: []string{"/docker-entrypoint.sh"},
		Cmd:        []string{"nginx", "-g", "daemon off;"},
		Env:        []string{"GREETING=hello world"},
		WorkingDir: "/srv",
		User:       "nginx",
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"}; !slices.Equal(c.Args, want) {
		t.Errorf("args = %q, want %q", c.Args, want)
	}
	if c.WorkingDir != "/srv" || c.User != "nginx" {
		t.Errorf("unexpected config: %+v", c)
	}
}

func TestFromImageNoCommand(t *testing.T) {
	if _, err := FromImage(ocispec.ImageConfig{}); err == nil {
		t.Fatal("expected an error for an image without a command")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	// Every field is set, so one that doesn't survive the trip to init,
	// e.g. with a mistyped JSON name, is caught.
	want := Config{
		Args:       []string{"sh", "-c", "echo 'hi'"},
		Env:        []string{"A=b c"},
		WorkingDir: "/w",
		User:       "1000:1000",
		Shutdown:   ShutdownPowerOff,
		TTY:        true,
		Rows:       40,
		Columns:    120,
		Stdin:      true,
		Mounts:     []Mount{{Tag: "c2vm0", Target: "/src"}, {Tag: "c2vm1", Target: "/data", ReadOnly: true}},
	}
	got := roundTrip(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestConfigShutdownDefault(t *testing.T) {
	if got := roundTrip(t, Config{Args: []string{"true"}}); got.Shutdown != ShutdownReboot {
		t.Errorf("shutdown = %q, want %q", got.Shutdown, ShutdownReboot)
	}
}

func TestConfigShutdownInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"args": ["true"], "shutdown": "halt"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err == nil {
		t.Error("expected an error for an unknown shutdown")
	}
}

func roundTrip(t *testing.T, c Config) Config {
	t.Helper()
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEnviron(t *testing.T) {
	env := Environ([]string{"FOO=bar"}, "/home/app")
	if v, _ := Getenv(env, "PATH"); v != DefaultPath {
		t.Errorf("PATH = %q, want the default", v)
	}
	if v, _ := Getenv(env, "HOME"); v != "/home/app" {
		t.Errorf("HOME = %q", v)
	}

	env = Environ([]string{"PATH=/opt/bin", "HOME=/data"}, "/home/app")
	if v, _ := Getenv(env, "PATH"); v != "/opt/bin" {
		t.Errorf("PATH = %q, want the image's", v)
	}
	if v, _ := Getenv(env, "HOME"); v != "/data" {
		t.Errorf("HOME = %q, want the image's", v)
	}
}

func TestLookPath(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "app"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "app"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	path := dir1 + string(os.PathListSeparator) + dir2

	got, err := LookPath("app", path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir2, "app"); got != want {
		t.Errorf("LookPath = %q, want %q (the non-executable one should be skipped)", got, want)
	}

	if got, _ := LookPath("/abs/app", path); got != "/abs/app" {
		t.Errorf("absolute path changed to %q", got)
	}
	if _, err := LookPath("missing", path); err == nil {
		t.Error("expected an error for a missing command")
	}
}
