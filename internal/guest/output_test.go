package guest

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// connected returns the two ends of a stream socket, as a stand-in for
// init's vsock connection to the host.
func connected(t *testing.T) (guestEnd, hostEnd *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	guestEnd, hostEnd = os.NewFile(uintptr(fds[0]), "guest"), os.NewFile(uintptr(fds[1]), "host")
	t.Cleanup(func() {
		guestEnd.Close()
		hostEnd.Close()
	})
	return guestEnd, hostEnd
}

// receive reads n bytes from conn, as the host does, and sends them.
func receive(conn io.Reader, n int) <-chan []byte {
	got := make(chan []byte, 1)
	go func() {
		data := make([]byte, n)
		n, _ := io.ReadFull(conn, data)
		got <- data[:n]
	}()
	return got
}

type finished struct {
	sent int64
	err  error
}

// finish calls o.Finish, and fails if it doesn't return.
func finish(t *testing.T, o *Output) (int64, error) {
	t.Helper()
	results := make(chan finished, 1)
	go func() {
		sent, err := o.Finish()
		results <- finished{sent, err}
	}()
	select {
	case r := <-results:
		return r.sent, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("Finish didn't return")
		return 0, nil
	}
}

func TestOutput(t *testing.T) {
	guestEnd, hostEnd := connected(t)
	// More than the pipe and the socket hold, so the copy has to keep
	// up.
	want := make([]byte, 4<<20)
	rand.Read(want)
	got := receive(hostEnd, len(want))
	o, w, err := NewOutput(guestEnd)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(want); err != nil {
		t.Fatal(err)
	}
	w.Close()

	sent, err := finish(t, o)
	if err != nil {
		t.Errorf("Finish: %v", err)
	}
	if sent != int64(len(want)) {
		t.Errorf("Finish sent %d bytes, want %d", sent, len(want))
	}
	if data := <-got; !bytes.Equal(data, want) {
		t.Errorf("the host got %d bytes, want the %d written", len(data), len(want))
	}
}

func TestOutputNothing(t *testing.T) {
	guestEnd, _ := connected(t)
	o, w, err := NewOutput(guestEnd)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if sent, err := finish(t, o); sent != 0 || err != nil {
		t.Errorf("Finish() = %d, %v, want 0, nil", sent, err)
	}
}

// Finish waits for the pipe's end: what the last process holding it
// writes before it goes away arrives whole, though Finish was called
// before.
func TestOutputUntilEnd(t *testing.T) {
	guestEnd, hostEnd := connected(t)
	first, last := []byte("first words\n"), []byte("last words\n")
	want := append(append([]byte{}, first...), last...)
	got := receive(hostEnd, len(want))
	o, w, err := NewOutput(guestEnd)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(first); err != nil {
		t.Fatal(err)
	}
	results := make(chan finished, 1)
	go func() {
		sent, err := o.Finish()
		results <- finished{sent, err}
	}()
	// Finish can't return while the write end is open, so this doesn't
	// depend on timing: the wait only gives a Finish that returns early
	// the time to.
	select {
	case <-results:
		t.Fatal("Finish returned before the pipe ended")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := w.Write(last); err != nil {
		t.Fatal(err)
	}
	w.Close()

	var r finished
	select {
	case r = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("Finish didn't return once the pipe ended")
	}
	if r.err != nil {
		t.Errorf("Finish: %v", r.err)
	}
	if r.sent != int64(len(want)) {
		t.Errorf("Finish sent %d bytes, want %d", r.sent, len(want))
	}
	if data := <-got; !bytes.Equal(data, want) {
		t.Errorf("the host got %q, want %q", data, want)
	}
}

// When the host stops reading, the command gets EPIPE, or SIGPIPE.
func TestOutputHostCloses(t *testing.T) {
	guestEnd, hostEnd := connected(t)
	hostEnd.Close()
	o, w, err := NewOutput(guestEnd)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	buf := make([]byte, 32*1024)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := w.Write(buf)
		if errors.Is(err, syscall.EPIPE) {
			break
		}
		if err != nil {
			t.Fatalf("write: %v, want EPIPE", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("writes still succeed after the host closed the connection")
		}
	}

	if _, err := finish(t, o); err != nil {
		t.Errorf("Finish: %v, want nil: the host going away isn't an error", err)
	}
}
