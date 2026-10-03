// Package rootfs creates the ext4 image used as the VM's root drive.
package rootfs

import (
	"fmt"
	"log"
	"os"
	"os/exec"
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

// runCommand runs a command, and includes its output in the error if it
// fails.
func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
