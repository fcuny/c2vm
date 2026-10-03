// Package rootfs creates the ext4 image used as the VM's root drive.
package rootfs

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/renameio/v2"
)

// Create writes an empty ext4 filesystem of the given size (as
// understood by fallocate, e.g. "2G") to path.
func Create(path, size string) error {
	f, err := renameio.NewPendingFile(path)
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if err := runCommand("fallocate", "-l", size, f.Name()); err != nil {
		return err
	}

	if err := runCommand("mkfs.ext4", "-F", f.Name()); err != nil {
		return err
	}

	return f.CloseAtomicallyReplace()
}

// Mount loop-mounts the image at path on a new temporary directory. The
// caller must call unmount once done, which also removes the directory.
func Mount(path string) (dir string, unmount func() error, err error) {
	dir, err = os.MkdirTemp("", "c2vm")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create mount temp dir: %w", err)
	}

	if err := runCommand("mount", "-o", "loop", path, dir); err != nil {
		os.Remove(dir)
		return "", nil, err
	}
	log.Printf("mounted %s on %s\n", path, dir)

	unmount = func() error {
		log.Printf("umount %s\n", dir)
		if err := runCommand("umount", dir); err != nil {
			return err
		}
		return os.Remove(dir)
	}
	return dir, unmount, nil
}

// Shrink checks the filesystem at path and resizes it to its minimum
// size.
func Shrink(path string) error {
	if err := runCommand("e2fsck", "-p", "-f", path); err != nil {
		return err
	}

	return runCommand("resize2fs", "-M", path)
}

// WriteExtraFiles adds the files the VM needs on top of the container's
// filesystem, mounted at dir.
func WriteExtraFiles(dir string) error {
	etc := filepath.Join(dir, "etc")
	if err := os.MkdirAll(etc, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etc, "hosts"), []byte("127.0.0.1\tlocalhost\n"), 0644); err != nil {
		return err
	}

	// The firecracker SDK passes the nameservers from the CNI result to
	// the kernel's "ip=" boot parameter, and the kernel exposes them in
	// /proc/net/pnp in resolv.conf format.
	resolvConf := filepath.Join(etc, "resolv.conf")
	if err := os.Remove(resolvConf); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink("/proc/net/pnp", resolvConf)
}

// runCommand runs a command, and includes its output in the error if it
// fails.
func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
