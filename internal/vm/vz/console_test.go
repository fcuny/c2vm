//go:build darwin

package vz

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// syncBuffer is a bytes.Buffer the console's goroutine can write to
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const panicOutput = "[    0.820843] Kernel panic - not syncing: sysrq triggered crash\r\n" +
	"[    0.821054] Call trace:\r\n" +
	"[    0.822319] ---[ end Kernel panic - not syncing: sysrq triggered crash ]---\r\n"

func panicked(c *console) bool {
	select {
	case <-c.panicked:
		return true
	default:
		return false
	}
}

func TestConsoleScan(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   bool
	}{
		{"no panic", []string{"c2vm-init: eth0: 192.168.64.7/24\r\n", "hello\r\n"}, false},
		{"one write", []string{panicOutput}, true},
		{"each byte", strings.Split(panicOutput, ""), true},
		{"start only", []string{"Kernel panic - not syncing: oops\r\n"}, false},
		{"end split", []string{panicOutput[:len(panicOutput)-50], panicOutput[len(panicOutput)-50:]}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &console{w: &syncBuffer{}, panicked: make(chan struct{})}
			for _, w := range tt.writes {
				c.scan([]byte(w))
			}
			if got := panicked(c); got != tt.want {
				t.Errorf("panicked = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestConsolePanicGrace checks a panic is reported even if the kernel
// never prints its end.
func TestConsolePanicGrace(t *testing.T) {
	c := &console{w: &syncBuffer{}, panicked: make(chan struct{})}
	c.scan([]byte("Kernel panic - not syncing: Attempted to kill init!\r\n"))
	select {
	case <-c.panicked:
	case <-time.After(panicGrace + 5*time.Second):
		t.Fatal("the panic wasn't reported")
	}
}

// TestConsoleCopy checks the console's output arrives whole, while the
// pipe's write end stays open, as Virtualization.framework keeps it.
func TestConsoleCopy(t *testing.T) {
	var out syncBuffer
	c, err := watchConsole(&out)
	if err != nil {
		t.Fatal(err)
	}

	// More than the pipe holds: the write returns once the copy has
	// read most of it.
	want := strings.Repeat("x", 1<<20)
	if _, err := c.port.output.WriteString(want); err != nil {
		t.Fatal(err)
	}
	c.drain()
	if got := out.String(); got != want {
		t.Errorf("copied %d bytes, want %d", len(got), len(want))
	}
}

// TestConsoleDrain checks drain copies what's still in the pipe when
// the copy stops.
func TestConsoleDrain(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// No copy running: everything is left for drain.
	done := make(chan struct{})
	close(done)
	var out syncBuffer
	c := &console{r: r, w: &out, done: done, panicked: make(chan struct{})}

	if _, err := w.WriteString(panicOutput); err != nil {
		t.Fatal(err)
	}
	c.drain()
	if got := out.String(); got != panicOutput {
		t.Errorf("drained %q, want %q", got, panicOutput)
	}
	if !panicked(c) {
		t.Error("the panic in the drained output wasn't reported")
	}
}

// TestConsoleLines checks another writer to the console's writer can't
// land in the middle of a console line the guest's tty passed in
// pieces, as the command's stderr did.
func TestConsoleLines(t *testing.T) {
	var out syncBuffer
	stderr := &lockedWriter{w: &out}
	c := &console{w: stderr, panicked: make(chan struct{})}

	c.write([]byte("c2vm-init: exiting with status 5, "))
	c.write([]byte("shutting down"))
	if _, err := stderr.Write([]byte("err\n")); err != nil {
		t.Fatal(err)
	}
	c.write([]byte("\r\nc2vm-init: next"))
	c.write([]byte(" line\r\n"))

	want := "err\nc2vm-init: exiting with status 5, shutting down\r\nc2vm-init: next line\r\n"
	if got := out.String(); got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestConsoleLongLine checks a line longer than maxLine is written
// without waiting for its end, and the rest of it once drained.
func TestConsoleLongLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	done := make(chan struct{})
	close(done)
	var out syncBuffer
	c := &console{r: r, w: &out, done: done, panicked: make(chan struct{})}

	long := strings.Repeat("x", maxLine+10)
	// The long line's x's are left out of what's reported.
	short := func(s string) string { return strings.Replace(s, long, "<long>", 1) }
	c.write([]byte(long))
	if got := out.String(); got != long {
		t.Errorf("wrote %d bytes of a line with no end, want %d", len(got), len(long))
	}
	c.write([]byte("\npartial"))
	if got, want := out.String(), long+"\n"; got != want {
		t.Errorf("wrote %q, want %q", short(got), short(want))
	}
	c.drain()
	if got, want := out.String(), long+"\npartial"; got != want {
		t.Errorf("after drain, wrote %q, want %q", short(got), short(want))
	}

	// A line that ends, then one longer than maxLine that doesn't, in
	// one read: neither waits.
	var out2 syncBuffer
	c = &console{w: &out2, panicked: make(chan struct{})}
	both := "a\n" + long
	c.write([]byte(both))
	if got := out2.String(); got != both {
		t.Errorf("wrote %d bytes, want %d", len(got), len(both))
	}
}

// TestConsoleLineWait checks the start of a line is written once it has
// waited lineWait for its end, without drain: c2vm may be killed before
// it drains.
func TestConsoleLineWait(t *testing.T) {
	var out syncBuffer
	c := &console{w: &out, panicked: make(chan struct{}), lineWait: time.Millisecond}
	c.write([]byte("done\nstuck at"))
	for deadline := time.Now().Add(10 * time.Second); out.String() != "done\nstuck at"; {
		if time.Now().After(deadline) {
			t.Fatalf("wrote %q, want %q", out.String(), "done\nstuck at")
		}
		time.Sleep(time.Millisecond)
	}
	// The line's end comes after its start was written.
	c.write([]byte(" boot\n"))
	if got, want := out.String(), "done\nstuck at boot\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestConsoleStaleWait checks a timer that fired for a line that has
// ended since doesn't write the next line's start early.
func TestConsoleStaleWait(t *testing.T) {
	var out syncBuffer
	c := &console{w: &out, panicked: make(chan struct{}), lineWait: time.Hour}
	c.write([]byte("first\nsecond"))
	defer c.held.Stop()
	// What the first line's timer runs, if it fired while write held mu.
	c.flushDue()
	if got, want := out.String(), "first\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestWaitWriter checks waitWriter waits for a full non-blocking pipe,
// instead of dropping what doesn't fit.
func TestWaitWriter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Fd makes w blocking: make it non-blocking again, as a terminal
	// shared with nonblockingInput's stdin would be.
	fd := int(w.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}

	want := strings.Repeat("x", 1<<20)
	errs := make(chan error, 1)
	go func() {
		_, err := waitWriter{fd}.Write([]byte(want))
		w.Close()
		errs <- err
	}()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("read %d bytes, want %d", len(got), len(want))
	}
}
