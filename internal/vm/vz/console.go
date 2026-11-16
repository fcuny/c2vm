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
	// mu guards line, held and lineWait's timer: the timer flushes the
	// line from its own goroutine.
	mu sync.Mutex
	// line is the start of a line not written yet: see write.
	line []byte
	// lineWait is how long the start of a line waits for its end before
	// it's written anyway; zero holds it until drain. held flushes it
	// from due on.
	lineWait time.Duration
	held     *time.Timer
	due      time.Time
}

// maxLine is the most of a line the console holds before writing it
// anyway, for output that has no newlines.
const maxLine = 4096

// lineWait is how long the console holds the start of a line, so
// what's printed before a hang shows up, even if c2vm is killed then.
// The guest's tty passes a line's pieces within microseconds.
const lineWait = 100 * time.Millisecond

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
		lineWait: lineWait,
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

// write writes whole lines of p to w, each in one write, and holds the
// rest until its line ends, or for lineWait. w is shared with the
// command's stderr, and the guest's tty may pass a line and its "\r\n"
// in separate reads: the command's output would otherwise land in the
// middle of the line.
func (c *console) write(p []byte) {
	if len(p) == 0 {
		return
	}
	c.scan(p)

	c.mu.Lock()
	defer c.mu.Unlock()
	wasHeld := len(c.line) > 0
	c.line = append(c.line, p...)
	end := bytes.LastIndexByte(c.line, '\n') + 1
	if len(c.line)-end >= maxLine {
		end = len(c.line)
	}
	if end > 0 {
		// Like the guest's writes to the console, these don't fail
		// when nothing reads them.
		_, _ = c.w.Write(c.line[:end])
		c.line = append(c.line[:0], c.line[end:]...)
	}
	// The wait starts with the line, so one that grows a byte at a
	// time is still written.
	switch {
	case len(c.line) == 0:
		if c.held != nil {
			c.held.Stop()
		}
	case end > 0 || !wasHeld:
		c.holdLine()
	}
}

// holdLine starts the wait for the end of the line that was just
// started.
func (c *console) holdLine() {
	if c.lineWait == 0 {
		return
	}
	c.due = time.Now().Add(c.lineWait)
	if c.held == nil {
		c.held = time.AfterFunc(c.lineWait, c.flushDue)
		return
	}
	c.held.Reset(c.lineWait)
}

// flushDue writes what's held of a line once it has waited lineWait.
// The timer may have fired for a line that ended since, while write
// held mu: Reset doesn't stop a run that started, so check.
func (c *console) flushDue() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.due) {
		return
	}
	c.flushLocked()
}

// flush writes what's held of a line that hasn't ended.
func (c *console) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

func (c *console) flushLocked() {
	if len(c.line) > 0 {
		_, _ = c.w.Write(c.line)
		c.line = c.line[:0]
	}
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
		c.flush()
		c.mu.Lock()
		if c.held != nil {
			c.held.Stop()
		}
		c.mu.Unlock()
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

// lockedWriter writes to w for more than one goroutine, one write at a
// time, so one's write isn't split by another's.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
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
