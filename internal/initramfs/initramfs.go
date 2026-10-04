// Package initramfs builds the initramfs c2vm boots: c2vm-init as /init,
// and the configuration it runs the image's command from.
package initramfs

import (
	"io"
	"os"

	"fcuny.net/c2vm/internal/guest"
)

// Write writes an initramfs to w, with the init binary at initBinary.
func Write(w io.Writer, initBinary string, config guest.Config) error {
	init, err := os.ReadFile(initBinary)
	if err != nil {
		return err
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
	return c.close()
}
