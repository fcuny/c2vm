// Package initramfs builds the initramfs c2vm boots: c2vm-init as /init,
// the configuration it runs the image's command from, and, when the VM
// has a scratch disk, the mkfs.ext4 init formats it with.
package initramfs

import (
	"errors"
	"io"

	"fcuny.net/c2vm/internal/guest"
)

// Write writes an initramfs to w, with init as the init binary. mkfs is
// the static mkfs.ext4 init formats config's scratch disk with: it's
// needed with a scratch disk, and only then.
func Write(w io.Writer, init []byte, config guest.Config, mkfs []byte) error {
	if config.Scratch != nil && len(mkfs) == 0 {
		return errors.New("a scratch disk needs mkfs.ext4 to format it")
	}
	if config.Scratch == nil && len(mkfs) != 0 {
		return errors.New("mkfs.ext4 is only for a scratch disk")
	}
	configData, err := config.Marshal()
	if err != nil {
		return err
	}

	c := &cpioWriter{w: w}
	for _, dir := range []string{"/dev", "/proc", "/sys", "/c2vm", guest.LowerDir, guest.WritableDir, guest.NewRootDir} {
		c.dir(dir, 0o755)
	}
	// The kernel opens /dev/console for init's stdin, stdout and stderr
	// before devtmpfs is mounted.
	c.charDev("/dev/console", 0o600, 5, 1)
	c.file("/init", 0o755, init)
	c.file(guest.ConfigPath, 0o644, configData)
	// Executable whatever mode it had where c2vm found it: a workflow
	// artifact, e.g., doesn't keep modes.
	if len(mkfs) != 0 {
		c.file(guest.MkfsPath, 0o755, mkfs)
	}
	return c.close()
}
