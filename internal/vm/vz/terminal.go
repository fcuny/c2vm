//go:build darwin

package vz

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"fcuny.net/c2vm/internal/guest"
)

// escapeKey stops the VM when the command is interactive, as Ctrl-C
// goes to the guest: Ctrl-], as with telnet.
const escapeKey = 0x1d

// terminal is the host's terminal, in raw mode while an interactive
// command runs.
type terminal struct {
	fd    int
	state *term.State
	once  sync.Once
}

// rawTerminal puts f's terminal in raw mode, so every key goes to the
// guest, which echoes and processes them.
func rawTerminal(f *os.File) (*terminal, error) {
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	log.SetOutput(crlfWriter{os.Stderr})
	return &terminal{fd: fd, state: state}, nil
}

// restore restores the terminal's previous mode. It's safe to call more
// than once.
func (t *terminal) restore() {
	t.once.Do(func() {
		log.SetOutput(os.Stderr)
		_ = term.Restore(t.fd, t.state)
		// Virtualization.framework makes the terminal non-blocking,
		// which the shell we return to doesn't expect.
		_ = unix.SetNonblock(t.fd, false)
	})
}

// crlfWriter ends lines with \r\n: in raw mode, the terminal doesn't
// return to the start of the line on \n.
type crlfWriter struct{ w io.Writer }

func (c crlfWriter) Write(p []byte) (int, error) {
	if _, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sendResizes sends the size of the terminal fd to init once it
// connects, and again whenever it changes, until stop is called.
func sendResizes(machine *vz.VirtualMachine, fd int) (stop func(), err error) {
	listener, err := listen(machine, guest.ResizePort)
	if err != nil {
		return nil, err
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			// The size can change between the configuration and
			// init connecting: always start with the current one.
			if columns, rows, err := term.GetSize(fd); err == nil {
				if _, err := conn.Write(guest.EncodeSize(uint16(rows), uint16(columns))); err != nil {
					return
				}
			}
			select {
			case <-winch:
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(winch)
		close(done)
		listener.Close()
	}, nil
}

// nonblockingInput returns a copy of f that Go reads with its poller.
// Virtualization.framework makes the console's output non-blocking, and
// on a terminal, stdin and stdout share that mode: reading os.Stdin,
// which Go opened blocking, would then fail with EAGAIN.
func nonblockingInput(f *os.File) (*os.File, error) {
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// forwardInput copies in to the returned file, for the console to read,
// until escapeKey is pressed, which closes the returned channel.
func forwardInput(in io.Reader) (*os.File, <-chan struct{}, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	escaped := make(chan struct{})
	go func() {
		defer w.Close()
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if i := bytes.IndexByte(buf[:n], escapeKey); i >= 0 {
				_, _ = w.Write(buf[:i])
				close(escaped)
				return
			}
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return r, escaped, nil
}
