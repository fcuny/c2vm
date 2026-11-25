//go:build linux

package main

import (
	"errors"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// The tests use TCP on loopback, which connect handles as it does vsock:
// a nonblocking connect returns EINPROGRESS, and SO_ERROR tells how it
// ended.

// listen returns a listener on loopback and its address.
func listen(t *testing.T) (net.Listener, *unix.SockaddrInet4) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, &unix.SockaddrInet4{Port: l.Addr().(*net.TCPAddr).Port, Addr: [4]byte{127, 0, 0, 1}}
}

// nonblockingSocket returns a nonblocking TCP socket, closed at the end
// of the test.
func nonblockingSocket(t *testing.T) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return fd
}

// countPolls makes poll count its calls, and fail with EINTR the first
// interrupted times, for the rest of the test.
func countPolls(t *testing.T, interrupted int) *int {
	t.Helper()
	calls := 0
	t.Cleanup(func() { poll = unix.Poll })
	poll = func(fds []unix.PollFd, timeout int) (int, error) {
		calls++
		if calls <= interrupted {
			return -1, unix.EINTR
		}
		return unix.Poll(fds, timeout)
	}
	return &calls
}

func checkConnected(t *testing.T, fd int, l net.Listener) {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_NONBLOCK != 0 {
		t.Error("socket still nonblocking after connect")
	}
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := unix.Write(fd, []byte{'x'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf [1]byte
	if _, err := conn.Read(buf[:]); err != nil || buf[0] != 'x' {
		t.Fatalf("read %q, %v; want \"x\"", buf[:], err)
	}
}

func TestConnect(t *testing.T) {
	l, sa := listen(t)
	fd := nonblockingSocket(t)
	calls := countPolls(t, 0)

	if err := connect(fd, sa); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// At least: a real signal can interrupt the real poll too.
	if *calls < 1 {
		t.Errorf("poll called %d times, want at least 1: connect didn't wait in poll", *calls)
	}
	checkConnected(t, fd, l)
}

// TestConnectInterrupted checks that connect waits again when a signal
// interrupts poll, on the same connection.
func TestConnectInterrupted(t *testing.T) {
	l, sa := listen(t)
	fd := nonblockingSocket(t)
	calls := countPolls(t, 2)

	if err := connect(fd, sa); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if *calls < 3 {
		t.Errorf("poll called %d times, want at least 3", *calls)
	}
	checkConnected(t, fd, l)
}

func TestConnectRefused(t *testing.T) {
	l, sa := listen(t)
	l.Close()
	fd := nonblockingSocket(t)

	err := connect(fd, sa)
	if !errors.Is(err, unix.ECONNREFUSED) {
		t.Fatalf("connect: %v, want ECONNREFUSED", err)
	}
}
