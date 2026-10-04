package initramfs

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"fcuny.net/c2vm/internal/guest"
)

type entry struct {
	name             string
	mode             uint32
	rdevMaj, rdevMin uint32
	data             []byte
}

// readCPIO parses a newc archive, checking the alignment the kernel
// expects along the way.
func readCPIO(t *testing.T, b []byte) []entry {
	t.Helper()
	var entries []entry
	off := 0
	field := func(i int) uint32 {
		v, err := strconv.ParseUint(string(b[off+6+8*i:off+6+8*(i+1)]), 16, 32)
		if err != nil {
			t.Fatalf("bad header field %d at offset %d: %v", i, off, err)
		}
		return uint32(v)
	}
	for {
		if off%4 != 0 {
			t.Fatalf("header at offset %d isn't 4-byte aligned", off)
		}
		if string(b[off:off+6]) != "070701" {
			t.Fatalf("bad magic at offset %d: %q", off, b[off:off+6])
		}
		mode, size, rdevMaj, rdevMin, nameSize := field(1), field(6), field(9), field(10), field(11)
		name := string(b[off+110 : off+110+int(nameSize)-1])
		off = align(off + 110 + int(nameSize))
		data := b[off : off+int(size)]
		off = align(off + int(size))
		if name == "TRAILER!!!" {
			if off != len(b) {
				t.Fatalf("%d bytes after the trailer", len(b)-off)
			}
			return entries
		}
		entries = append(entries, entry{name, mode, rdevMaj, rdevMin, data})
	}
}

func align(n int) int { return (n + 3) &^ 3 }

func TestWrite(t *testing.T) {
	// An init binary whose size isn't a multiple of 4 exercises padding.
	initBinary := filepath.Join(t.TempDir(), "c2vm-init")
	if err := os.WriteFile(initBinary, []byte("not really an ELF"), 0755); err != nil {
		t.Fatal(err)
	}
	config := guest.Config{Args: []string{"nginx", "-g", "daemon off;"}, User: "nginx", Shutdown: guest.ShutdownPowerOff}

	var buf bytes.Buffer
	if err := Write(&buf, initBinary, config); err != nil {
		t.Fatal(err)
	}
	entries := readCPIO(t, buf.Bytes())

	byName := map[string]entry{}
	var names []string
	for _, e := range entries {
		byName[e.name] = e
		names = append(names, e.name)
	}

	// Parents have to come before what's in them.
	for _, dir := range []string{"dev", "proc", "sys", "c2vm", "c2vm/lower", "c2vm/rw", "c2vm/root"} {
		e, ok := byName[dir]
		if !ok {
			t.Errorf("missing directory %s in %q", dir, names)
			continue
		}
		if e.mode != modeDir|0o755 {
			t.Errorf("%s has mode %o", dir, e.mode)
		}
	}
	if slices.Index(names, "c2vm") > slices.Index(names, "c2vm/config.json") {
		t.Error("c2vm/config.json comes before its directory")
	}

	if e := byName["dev/console"]; e.mode != modeCharDev|0o600 || e.rdevMaj != 5 || e.rdevMin != 1 {
		t.Errorf("dev/console = %+v, want character device 5:1", e)
	}

	if e := byName["init"]; e.mode != modeRegular|0o755 || string(e.data) != "not really an ELF" {
		t.Errorf("init = mode %o, %q", e.mode, e.data)
	}

	var got guest.Config
	if err := json.Unmarshal(byName["c2vm/config.json"].data, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Args, config.Args) || got.User != "nginx" || got.Shutdown != guest.ShutdownPowerOff {
		t.Errorf("config = %+v", got)
	}
}

// TestWriteSystemCPIO checks the archive with the system's cpio, as a
// second opinion on the format.
func TestWriteSystemCPIO(t *testing.T) {
	cpio, err := exec.LookPath("cpio")
	if err != nil {
		t.Skip("cpio not found")
	}

	initBinary := filepath.Join(t.TempDir(), "c2vm-init")
	if err := os.WriteFile(initBinary, []byte("init"), 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, initBinary, guest.Config{Args: []string{"true"}}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(cpio, "-t")
	cmd.Stdin = &buf
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cpio -t: %v\n%s", err, out)
	}
	for _, name := range []string{"init", "c2vm/config.json", "dev/console"} {
		if !bytes.Contains(out, []byte(name+"\n")) {
			t.Errorf("cpio -t doesn't list %s:\n%s", name, out)
		}
	}
}
