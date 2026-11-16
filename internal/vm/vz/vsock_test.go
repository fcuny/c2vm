//go:build darwin

package vz

import (
	"errors"
	"io"
	"log"
	"net"
	"os"
	"testing"

	"fcuny.net/c2vm/internal/guest"
)

// fakeListener hands out one end of a pipe, as if init had connected.
type fakeListener struct{ guest net.Conn }

func (l *fakeListener) Accept() (net.Conn, error) {
	host, guest := net.Pipe()
	l.guest = guest
	return host, nil
}

func isConnected(h *vsockHost) bool {
	select {
	case <-h.connected:
		return true
	default:
		return false
	}
}

func TestVsockHostConnected(t *testing.T) {
	h := newVsockHost(nil)
	if isConnected(h) {
		t.Fatal("connected before init connected")
	}
	l := &fakeListener{}
	conn, err := h.accept(l)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()
	if !isConnected(h) {
		t.Error("not connected once init connected")
	}
	// A second connection mustn't close connected again.
	if _, err := h.accept(&fakeListener{}); err != nil {
		t.Fatalf("second accept: %v", err)
	}
	// Init is up: the boot isn't stuck.
	if h.abandon() {
		t.Error("abandon gave up on a boot whose init connected")
	}
	if h.closedEarly() {
		t.Error("closedEarly after a failed abandon")
	}
}

// answer runs answerUp on the host's end of a pipe, and returns what
// init reads on the other: ReadUp's verdict.
func answer(h *vsockHost) error {
	host, guestEnd := net.Pipe()
	done := make(chan struct{})
	go func() {
		h.answerUp(host)
		host.Close()
		close(done)
	}()
	err := guest.ReadUp(guestEnd)
	<-done
	return err
}

func TestAnswerUp(t *testing.T) {
	h := newVsockHost(nil)
	if err := answer(h); err != nil {
		t.Fatalf("init read %v, want the host's answer", err)
	}
	if !isConnected(h) {
		t.Error("not connected once init has its answer")
	}
	if h.abandon() {
		t.Error("abandon gave up on a boot whose init has its answer")
	}
	// A guest that reboots runs init again, which gets an answer too.
	if err := answer(h); err != nil {
		t.Errorf("init read %v after a reboot, want the host's answer", err)
	}
}

// TestAnswerUpAbandoned checks init learns the host gave up, and waits
// to be stopped, rather than run the command in a VM being replaced.
func TestAnswerUpAbandoned(t *testing.T) {
	h := newVsockHost(nil)
	h.abandon()
	if err := answer(h); !errors.Is(err, guest.ErrHostGaveUp) {
		t.Errorf("init read %v, want %v", err, guest.ErrHostGaveUp)
	}
	if isConnected(h) {
		t.Error("connected after the host gave up")
	}
}

// TestAnswerUpWriteFails checks a boot whose init didn't get its answer
// isn't up: the deadline then restarts it, rather than leave it waiting
// forever.
func TestAnswerUpWriteFails(t *testing.T) {
	// answerUp logs the failed write.
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newVsockHost(nil)
	host, guestEnd := net.Pipe()
	guestEnd.Close()
	h.answerUp(host)
	if isConnected(h) {
		t.Error("connected though init didn't get the answer")
	}
	if !h.abandon() {
		t.Error("abandon didn't give up on a boot whose init has no answer")
	}
}

// TestVsockHostAbandoned checks a connection that comes once the host
// gave up on the boot is refused and closed: the boot is being
// restarted, and mustn't take stdin from the next one.
func TestVsockHostAbandoned(t *testing.T) {
	h := newVsockHost(nil)
	if !h.abandon() {
		t.Fatal("abandon didn't give up on a boot whose init never connected")
	}
	l := &fakeListener{}
	conn, err := h.accept(l)
	if !errors.Is(err, errAbandoned) || conn != nil {
		t.Fatalf("accept after abandon = %v, %v, want nil, %v", conn, err, errAbandoned)
	}
	if !h.closedEarly() {
		t.Error("closedEarly is false after abandon: the refused accept would be logged")
	}
	if isConnected(h) {
		t.Error("connected after a refused connection")
	}
	// The guest's end sees the connection closed.
	if _, err := l.guest.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("guest's read after the refusal = %v, want EOF", err)
	}
}
