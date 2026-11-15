package macslot

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func acquire(t *testing.T, dir string, n int) *Slot {
	t.Helper()
	s, err := Acquire(dir, n)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s != nil {
		t.Cleanup(func() { s.Release() })
	}
	return s
}

func TestAcquireTakesFirstFreeSlot(t *testing.T) {
	dir := t.TempDir()

	a := acquire(t, dir, 4)
	b := acquire(t, dir, 4)
	if a == nil || b == nil {
		t.Fatalf("Acquire returned no slot with free ones: %v, %v", a, b)
	}
	if got := a.MAC[5]; got != 0 {
		t.Errorf("first slot's last byte = %d, want 0", got)
	}
	if got := b.MAC[5]; got != 1 {
		t.Errorf("second slot's last byte = %d, want 1", got)
	}
	if !bytes.Equal(a.MAC[:5], b.MAC[:5]) {
		t.Errorf("slots have different prefixes: %s, %s", a.MAC, b.MAC)
	}
}

func TestAcquireReusesReleasedSlot(t *testing.T) {
	dir := t.TempDir()

	a := acquire(t, dir, 4)
	b := acquire(t, dir, 4)
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	c := acquire(t, dir, 4)
	if c.MAC.String() != a.MAC.String() {
		t.Errorf("after releasing %s, got %s, want it back", a.MAC, c.MAC)
	}
	if c.MAC.String() == b.MAC.String() {
		t.Errorf("got %s, which is still held", c.MAC)
	}
}

func TestAcquireAllTaken(t *testing.T) {
	dir := t.TempDir()

	acquire(t, dir, 2)
	acquire(t, dir, 2)
	if s := acquire(t, dir, 2); s != nil {
		t.Errorf("Acquire with every slot taken = %s, want none", s.MAC)
	}
}

// An unreadable slot file stands for one another user created: the
// slot is skipped, not the whole directory.
func TestAcquireSkipsUnreadableSlot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any file")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slot-0.lock"), nil, 0); err != nil {
		t.Fatal(err)
	}

	s := acquire(t, dir, 4)
	if s == nil {
		t.Fatal("Acquire returned no slot, with three free")
	}
	if got := s.MAC[5]; got != 1 {
		t.Errorf("slot's last byte = %d, want 1", got)
	}
}

func TestAcquireAllUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any file")
	}
	dir := t.TempDir()
	for _, name := range []string{"slot-0.lock", "slot-1.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := Acquire(dir, 2); err == nil {
		if s != nil {
			s.Release()
		}
		t.Error("Acquire with no usable slot succeeded, want an error")
	}
}

// With one slot held and the other unreadable, every usable slot is in
// use: no slot, and no error.
func TestAcquireHeldAndUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any file")
	}
	dir := t.TempDir()
	acquire(t, dir, 1)
	if err := os.WriteFile(filepath.Join(dir, "slot-1.lock"), nil, 0); err != nil {
		t.Fatal(err)
	}
	s, err := Acquire(dir, 2)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s != nil {
		s.Release()
		t.Errorf("Acquire = %s, want no slot", s.MAC)
	}
}

func TestAcquireInvalidCount(t *testing.T) {
	for _, n := range []int{0, 257} {
		if _, err := Acquire(t.TempDir(), n); err == nil {
			t.Errorf("Acquire(%d) succeeded, want an error", n)
		}
	}
}

func TestPrefixIsStable(t *testing.T) {
	dir := t.TempDir()

	a := acquire(t, dir, 4)
	mac := a.MAC.String()
	a.Release()
	if !valid(a.MAC) {
		t.Errorf("%s isn't a locally administered, unicast MAC", mac)
	}

	path := filepath.Join(dir, "prefix")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("prefix file's mode = %v, want 0644, for other users to read it", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(data)), mac[:len(mac)-3]; got != want {
		t.Errorf("prefix file = %q, want %q", got, want)
	}

	if b := acquire(t, dir, 4); b.MAC.String() != mac {
		t.Errorf("second boot got %s, want %s", b.MAC, mac)
	}
}

func TestPrefixRace(t *testing.T) {
	dir := t.TempDir()

	const n = 16
	var wg sync.WaitGroup
	prefixes := make([][]byte, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { prefixes[i], errs[i] = loadPrefix(dir) })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("loadPrefix: %v", errs[i])
		}
		if !bytes.Equal(prefixes[i], prefixes[0]) {
			t.Errorf("loadPrefix returned %x and %x", prefixes[0], prefixes[i])
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir holds %v, want only the prefix", names)
	}
}

func TestInvalidPrefix(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":   "nope\n",
		"too short": "02:00:00:00\n",
		"multicast": "03:00:00:00:00\n",
		"universal": "00:00:00:00:00\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "prefix"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if s, err := Acquire(dir, 4); err == nil {
				s.Release()
				t.Errorf("Acquire with prefix %q succeeded, want an error", content)
			}
		})
	}
}

func TestNewPrefixBits(t *testing.T) {
	for range 100 {
		p, err := newPrefix()
		if err != nil {
			t.Fatal(err)
		}
		if !valid(append(p, 0)) {
			t.Fatalf("newPrefix = %x, not locally administered and unicast", p)
		}
	}
}
