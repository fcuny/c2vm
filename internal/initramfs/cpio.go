package initramfs

import (
	"fmt"
	"io"
	"strings"
)

// cpioWriter writes a cpio archive in the "newc" format, the one the
// kernel unpacks initramfs from.
type cpioWriter struct {
	w   io.Writer
	ino uint32
	err error
}

const (
	modeDir     = 0o040000
	modeRegular = 0o100000
	modeCharDev = 0o020000
)

func (c *cpioWriter) header(name string, mode uint32, size int, rdevMajor, rdevMinor uint32) {
	if c.err != nil {
		return
	}
	c.ino++
	nlink := 1
	if mode&modeDir != 0 {
		nlink = 2
	}
	name = strings.TrimPrefix(name, "/")
	// The fields are: magic, inode, mode, uid, gid, nlink, mtime, file
	// size, device major and minor, rdev major and minor, name size
	// (including the trailing NUL), and checksum. Entries belong to root
	// and have no timestamp, so archives are reproducible.
	hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		c.ino, mode, 0, 0, nlink, 0, size, 0, 0, rdevMajor, rdevMinor, len(name)+1, 0)
	c.write([]byte(hdr + name + "\x00"))
	c.pad(len(hdr) + len(name) + 1)
}

func (c *cpioWriter) write(b []byte) {
	if c.err != nil {
		return
	}
	_, c.err = c.w.Write(b)
}

// pad aligns the archive to 4 bytes after n bytes were written.
func (c *cpioWriter) pad(n int) {
	if r := n % 4; r != 0 {
		c.write(make([]byte, 4-r))
	}
}

func (c *cpioWriter) dir(name string, perm uint32) {
	c.header(name, modeDir|perm, 0, 0, 0)
}

func (c *cpioWriter) file(name string, perm uint32, data []byte) {
	c.header(name, modeRegular|perm, len(data), 0, 0)
	c.write(data)
	c.pad(len(data))
}

func (c *cpioWriter) charDev(name string, perm, major, minor uint32) {
	c.header(name, modeCharDev|perm, 0, major, minor)
}

// close writes the trailer that ends the archive.
func (c *cpioWriter) close() error {
	c.header("TRAILER!!!", 0, 0, 0, 0)
	return c.err
}
