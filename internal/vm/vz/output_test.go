//go:build darwin

package vz

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// socketPair returns the two ends of a stream socket: the host's, and
// init's.
func socketPair(t *testing.T) (io.ReadCloser, *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	host, init := os.NewFile(uintptr(fds[0]), "host"), os.NewFile(uintptr(fds[1]), "init")
	t.Cleanup(func() {
		host.Close()
		init.Close()
	})
	return host, init
}

// waitFor calls o.wait(n), and fails if it doesn't return.
func waitFor(t *testing.T, o *output, n int64) error {
	t.Helper()
	errs := make(chan error, 1)
	go func() { errs <- o.wait(n) }()
	select {
	case err := <-errs:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("still waiting for %d bytes", n)
		return nil
	}
}

// TestOutputWait checks wait returns once the bytes init sent are
// written, though init never ends the connection.
func TestOutputWait(t *testing.T) {
	conn, init := socketPair(t)
	want := make([]byte, 1<<20)
	rand.Read(want)
	go init.Write(want)

	var got lockedWriter
	var buf bytes.Buffer
	got.w = &buf
	o := newOutput("stdout")
	go o.copy(&got, conn)
	if err := waitFor(t, o, int64(len(want))); err != nil {
		t.Errorf("wait: %v", err)
	}
	got.mu.Lock()
	defer got.mu.Unlock()
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("copied %d bytes, want the %d sent", buf.Len(), len(want))
	}
}

func TestOutputWaitNothing(t *testing.T) {
	o := newOutput("stdout")
	if err := waitFor(t, o, 0); err != nil {
		t.Errorf("wait(0): %v", err)
	}
}

// TestOutputCutShort checks wait fails when the connection ends before
// all of what init sent arrived.
func TestOutputCutShort(t *testing.T) {
	conn, init := socketPair(t)
	init.Write([]byte("short"))
	init.Close()

	o := newOutput("stdout")
	go o.copy(io.Discard, conn)
	err := waitFor(t, o, 10)
	if err == nil {
		t.Fatal("wait succeeded, want an error: 5 of 10 bytes arrived")
	}
	if want := "the command's stdout was cut short, after 5 of 10 bytes"; !strings.Contains(err.Error(), want) {
		t.Errorf("wait: %v, want it to say %q", err, want)
	}
}

// failingWriter fails every write with err.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// TestOutputWriteFails checks a failure to write the output, other than
// its reader going away, makes wait fail: the file c2vm's stdout goes to
// is cut short.
func TestOutputWriteFails(t *testing.T) {
	conn, init := socketPair(t)
	if _, err := init.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}

	o := newOutput("stdout")
	go o.copy(failingWriter{syscall.ENOSPC}, conn)
	err := waitFor(t, o, 4)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("wait: %v, want ENOSPC", err)
	}
}

// TestOutputReaderReset checks ECONNRESET, which a socket's reader
// going away may give, isn't an error, like EPIPE.
func TestOutputReaderReset(t *testing.T) {
	conn, init := socketPair(t)
	if _, err := init.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}

	o := newOutput("stdout")
	go o.copy(failingWriter{syscall.ECONNRESET}, conn)
	if err := waitFor(t, o, 4); err != nil {
		t.Errorf("wait: %v, want nil: the reader going away isn't an error", err)
	}
}

// Once c2vm's stdout goes away, the copy stops and closes the
// connection, for the command to get SIGPIPE: init's writes fail. Wait
// doesn't wait for the rest, and that isn't an error.
func TestOutputReaderGone(t *testing.T) {
	conn, init := socketPair(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r.Close()

	o := newOutput("stdout")
	go o.copy(waitWriter{int(w.Fd())}, conn)
	if _, err := init.Write([]byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, o, 1<<20); err != nil {
		t.Errorf("wait: %v, want nil: the reader going away isn't an error", err)
	}

	buf := make([]byte, 32*1024)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := init.Write(buf)
		if errors.Is(err, syscall.EPIPE) {
			break
		}
		if err != nil {
			t.Fatalf("init's write: %v, want EPIPE", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("init's writes still succeed")
		}
	}
}
