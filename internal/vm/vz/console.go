//go:build darwin

package vz

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The kernel starts and ends what it prints about a panic with these.
var (
	panicStart = []byte("Kernel panic - not syncing")
	panicEnd   = []byte("---[ end Kernel panic - not syncing")
)

// panicGrace is how long to wait for the end of a panic once it starts,
// in case the kernel never finishes printing it.
const panicGrace = time.Second

// console copies the kernel's console to the host, and reports a kernel
// panic. The kernel waits forever after a panic (panic=0): the host
// stops the VM. With panic=N, it would reboot, and
// Virtualization.framework restarts a guest that reboots without
// telling the host, so the command would run again and again. Only the
// kernel and init write to this console, so the command's output can't
// pass for a panic.
type console struct {
	// port is the console's side of the pipes, for the VM. Nothing
	// writes to idle, which keeps its input open.
	port serialPort
	idle *os.File

	r    *os.File
	w    io.Writer
	done chan struct{} // closed when copy returns

	// panicked is closed once the kernel has printed a panic.
	panicked chan struct{}
	once     sync.Once
	grace    *time.Timer // started with the panic
	// tail is the end of what was read, for markers split across reads.
	tail []byte
}

// watchConsole copies what the VM writes to the returned console's
// port to w, until drain is called. Nothing is written to the port's
// input.
func watchConsole(w io.Writer) (*console, error) {
	input, idle, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	r, output, err := os.Pipe()
	if err != nil {
		input.Close()
		idle.Close()
		return nil, err
	}
	c := &console{
		port:     serialPort{input: input, output: output},
		idle:     idle,
		r:        r,
		w:        w,
		done:     make(chan struct{}),
		panicked: make(chan struct{}),
	}
	go c.copy()
	return c, nil
}

func (c *console) copy() {
	defer close(c.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := c.r.Read(buf)
		c.write(buf[:n])
		if err != nil {
			return
		}
	}
}

func (c *console) write(p []byte) {
	if len(p) == 0 {
		return
	}
	c.scan(p)
	// Like the guest's writes to the console, these don't fail when
	// nothing reads them.
	_, _ = c.w.Write(p)
}

// scan looks for the start and end of a panic in p, which follows what
// was scanned before.
func (c *console) scan(p []byte) {
	buf := append(c.tail, p...)
	if c.grace == nil && bytes.Contains(buf, panicStart) {
		c.grace = time.AfterFunc(panicGrace, c.reportPanic)
	}
	if c.grace != nil && bytes.Contains(buf, panicEnd) {
		c.reportPanic()
	}
	keep := min(len(buf), len(panicEnd)-1)
	c.tail = append(c.tail[:0], buf[len(buf)-keep:]...)
}

func (c *console) reportPanic() {
	c.once.Do(func() { close(c.panicked) })
}

// drain copies what's left of the console's output, and stops. Call it
// once the VM has stopped. Virtualization.framework keeps its own copy of
// the pipe's write end, so the read end never gets EOF: read until the
// pipe is empty instead.
func (c *console) drain() {
	// The deadline stops the pending read, without losing what it read.
	_ = c.r.SetReadDeadline(time.Now())
	<-c.done
	defer func() {
		if c.grace != nil {
			c.grace.Stop()
		}
		for _, f := range []*os.File{c.r, c.port.input, c.port.output, c.idle} {
			f.Close()
		}
	}()

	// The pipe is non-blocking: Control reads it without waiting.
	rc, err := c.r.SyscallConn()
	if err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	_ = rc.Control(func(fd uintptr) {
		for {
			n, err := unix.Read(int(fd), buf)
			if n <= 0 || err != nil {
				return
			}
			c.write(buf[:n])
		}
	})
}

// waitWriter writes to fd, and waits when it's full instead of failing:
// on a terminal, stdin, stdout and stderr usually share one file
// description, which nonblockingInput and Virtualization.framework make
// non-blocking. It leaves fd's mode as it is.
type waitWriter struct{ fd int }

func (w waitWriter) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := unix.Write(w.fd, p[written:])
		if n > 0 {
			written += n
		}
		switch {
		case errors.Is(err, unix.EAGAIN):
			fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLOUT}}
			if _, err := unix.Poll(fds, -1); err != nil && !errors.Is(err, unix.EINTR) {
				return written, err
			}
		case errors.Is(err, unix.EINTR):
		case err != nil:
			return written, err
		}
	}
	return written, nil
}
