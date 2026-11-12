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
