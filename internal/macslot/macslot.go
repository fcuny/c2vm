// Package macslot gives VMs MAC addresses that later VMs reuse.
//
// vmnet's DHCP server gives each new MAC address a lease from a pool of
// 253 addresses, and only frees it when the lease expires. With a new
// random MAC per boot, a few hundred boots within the lease time take
// the whole pool, and the next VM never gets an address. The server
// gives a MAC it knows its lease back, so reusing MACs makes the pool
// only have to cover the VMs that run at the same time.
//
// A VM takes the first free slot, which it holds with a lock on the
// slot's file until it stops, and its MAC is derived from the slot's
// number. The MACs share a random prefix, created once per directory,
// so that two users of a Mac don't get the same MAC for the same slot.
package macslot

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Slots is how many slots c2vm uses: the most VMs it reuses MACs for
// at the same time. More VMs get random MACs.
const Slots = 64

// prefixLen is the length of the prefix, in bytes. The MAC's last byte
// is the slot's number, which caps the number of slots at 256.
const prefixLen = 5

// Slot is a slot a VM holds, and the MAC it gives the VM.
type Slot struct {
	MAC  net.HardwareAddr
	file *os.File
}

// Release frees the slot, for another VM to take. The VM that held it
// must have stopped, or the next one could get the same MAC while it
// still runs.
func (s *Slot) Release() error {
	return s.file.Close()
}

// Acquire takes the first free slot out of n in dir, creating dir and
// its prefix if needed. It returns a nil Slot if all n are taken.
func Acquire(dir string, n int) (*Slot, error) {
	if n < 1 || n > 256 {
		return nil, fmt.Errorf("invalid number of slots %d: expected 1 to 256", n)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	prefix, err := loadPrefix(dir)
	if err != nil {
		return nil, err
	}

	// A slot whose file another user created, and this one can't open,
	// is skipped. It's an error only if every slot was: then none is in
	// use, and none can be.
	var denied error
	held := false
	for i := range n {
		f, err := tryLock(filepath.Join(dir, "slot-"+strconv.Itoa(i)+".lock"))
		if errors.Is(err, fs.ErrPermission) {
			denied = err
			continue
		}
		if err != nil {
			return nil, err
		}
		if f == nil {
			held = true
			continue
		}
		mac := make(net.HardwareAddr, 0, prefixLen+1)
		mac = append(mac, prefix...)
		mac = append(mac, byte(i))
		return &Slot{MAC: mac, file: f}, nil
	}
	if held {
		return nil, nil
	}
	return nil, denied
}

// tryLock opens path and locks it, and returns nil if another process
// holds the lock. The lock goes with the file: closing it, or the
// process exiting, releases it. flock only needs the file open for
// reading, which also works on a lock file another user created, such
// as root with sudo.
func tryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return f, nil
}

// loadPrefix returns the prefix in dir, creating it first if there's
// none. Of two processes creating it at the same time, the first one to
// link its file in place wins, and both use its prefix.
func loadPrefix(dir string) ([]byte, error) {
	path := filepath.Join(dir, "prefix")
	prefix, err := readPrefix(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return prefix, err
	}

	prefix, err = newPrefix()
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".prefix-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(net.HardwareAddr(prefix).String() + "\n")
	// Once linked, the file is never rewritten: a crash mustn't leave it
	// empty.
	if err == nil {
		err = tmp.Sync()
	}
	// CreateTemp's 0600 would hide the prefix from other users: from
	// the user, if root created it with sudo.
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return readPrefix(path)
}

// readPrefix reads the prefix in path, written as hex bytes separated
// by colons, as a MAC.
func readPrefix(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// net.ParseMAC only takes whole MACs: add a byte for the slot.
	mac, err := net.ParseMAC(strings.TrimSpace(string(data)) + ":00")
	if err != nil || len(mac) != prefixLen+1 || !valid(mac) {
		return nil, fmt.Errorf("%s: invalid prefix %q, remove the file to create a new one", path, strings.TrimSpace(string(data)))
	}
	return mac[:prefixLen], nil
}

// newPrefix returns a random prefix for locally administered, unicast
// MACs.
func newPrefix() ([]byte, error) {
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	prefix[0] = prefix[0]&^0x01 | 0x02
	return prefix, nil
}

// valid reports whether mac is locally administered, so it can't clash
// with a vendor's, and unicast, as a network interface's has to be.
func valid(mac net.HardwareAddr) bool {
	return mac[0]&0x02 != 0 && mac[0]&0x01 == 0
}
