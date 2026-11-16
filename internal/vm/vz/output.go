//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync"

	"golang.org/x/sys/unix"
)

// output copies one of the command's outputs to the host's, and counts
// what it wrote, for the exit status to wait for it: init reports how
// many bytes it sent with the status. The connection's end can't tell
// it, as Virtualization.framework drops a guest's shutdown that arrives
// while it still holds data for the host.
type output struct {
	name    string // stdout or stderr
	mu      sync.Mutex
	changed *sync.Cond
	written int64
	stopped bool
	// err is why the copy stopped, but for the host's output going away.
	err error
}

func newOutput(name string) *output {
	o := &output{name: name}
	o.changed = sync.NewCond(&o.mu)
	return o
}

// receiveOutput copies what init sends on port, the command's output
// called name, to w, once it connects.
func receiveOutput(host *vsockHost, port uint32, name string, w io.Writer) (*output, error) {
	listener, err := host.listen(port)
	if err != nil {
		return nil, err
	}

	o := newOutput(name)
	go func() {
		defer listener.Close()
		conn, err := host.accept(listener)
		if err != nil {
			if !host.closedEarly() {
				log.Printf("waiting for the command's output: %v", err)
			}
			o.stop(err)
			return
		}
		o.copy(w, conn)
	}()
	return o, nil
}

// copy copies conn to w until either fails. It closes conn as soon as
// writing to w fails: the command then gets SIGPIPE on its next write,
// as it would in a pipeline. When the reader of c2vm's output went away
// (c2vm boot ... | head -1), that isn't an error; any other failure,
// like a full disk, is.
func (o *output) copy(w io.Writer, conn io.ReadCloser) {
	defer conn.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			written, werr := w.Write(buf[:n])
			o.add(written)
			// On a pipe the reader going away is EPIPE, and on a socket
			// it may be ECONNRESET.
			if errors.Is(werr, unix.EPIPE) || errors.Is(werr, unix.ECONNRESET) {
				o.stop(nil)
				return
			}
			if werr != nil {
				o.stop(fmt.Errorf("writing it: %w", werr))
				return
			}
		}
		if err != nil {
			o.stop(err)
			return
		}
	}
}

func (o *output) add(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.written += int64(n)
	o.changed.Broadcast()
}

func (o *output) stop(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stopped, o.err = true, err
	o.changed.Broadcast()
}

// wait waits until n bytes were written, or the copy stopped. It fails
// if the copy stopped short of them, unless the host's output went
// away.
func (o *output) wait(n int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for o.written < n && !o.stopped {
		o.changed.Wait()
	}
	if o.written < n && o.err != nil {
		return fmt.Errorf("the command's %s was cut short, after %d of %d bytes: %w", o.name, o.written, n, o.err)
	}
	return nil
}
