package rootfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteExtraFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"no /etc", func(t *testing.T, dir string) {}},
		{"image has a resolv.conf", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "etc"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "etc", "resolv.conf"), []byte("nameserver 8.8.8.8\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)

			if err := WriteExtraFiles(dir); err != nil {
				t.Fatal(err)
			}

			target, err := os.Readlink(filepath.Join(dir, "etc", "resolv.conf"))
			if err != nil {
				t.Fatal(err)
			}
			if target != "/proc/net/pnp" {
				t.Errorf("resolv.conf points to %q, want /proc/net/pnp", target)
			}

			hosts, err := os.ReadFile(filepath.Join(dir, "etc", "hosts"))
			if err != nil {
				t.Fatal(err)
			}
			if string(hosts) != "127.0.0.1\tlocalhost\n" {
				t.Errorf("unexpected hosts file: %q", hosts)
			}
		})
	}
}
