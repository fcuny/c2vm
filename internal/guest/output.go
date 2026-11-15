package guest

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// The vsock ports on the host that init sends the command's stdout and
// stderr to, when it isn't interactive. The host copies each to its own
// stdout and stderr. Init reports how many bytes it sent with the exit
// status (see Status), and the host acknowledges the status once it has
// written them.
const (
	StdoutPort = 1027
	StderrPort = 1028
)

// Output passes what the command writes to one of its outputs to the
// host. The command writes to a pipe, as it would with docker run:
// opening /dev/stdout, which is /proc/self/fd/1, works on a pipe, and
// fails on a socket.
type Output struct {
	r    *os.File // the pipe's read end
	conn io.Writer
	done chan struct{}
	// sent is how many bytes were written to conn.
	sent int64
	// err is set when the copy stopped on an error. The host closing
	// conn isn't one.
	err error
}

// NewOutput starts copying what's written to the returned pipe's write
// end, for the command, to conn. Close the write end once the command
// has it, and call Finish once the command has exited.
//
// If the host closes conn, as it does when its own stdout went away
// (c2vm boot ... | head -1), the copy closes the pipe: the command gets
// SIGPIPE on its next write, as it would in a pipeline. That isn't an
// error.
func NewOutput(conn io.Writer) (*Output, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	o := &Output{r: r, conn: conn, done: make(chan struct{})}
	go o.copy()
	return o, w, nil
}

func (o *Output) copy() {
	defer close(o.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := o.r.Read(buf)
		if n > 0 {
			if werr := o.send(buf[:n]); werr != nil {
				if !errors.Is(werr, errHostGone) {
					o.err = werr
				}
				o.r.Close()
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				o.err = fmt.Errorf("reading the command's output: %w", err)
			}
			return
		}
	}
}

// errHostGone is send's error once the host has closed the connection.
var errHostGone = errors.New("the host closed the connection")

func (o *Output) send(p []byte) error {
	n, err := o.conn.Write(p)
	o.sent += int64(n)
	if errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET) {
		return errHostGone
	}
	if err != nil {
		return fmt.Errorf("writing to the host: %w", err)
	}
	return nil
}

// Finish waits for the copy to reach the pipe's end, and returns how many
// bytes were sent in all, for the host to know when it has them. The
// pipe ends once every process holding its write end is gone: call
// Finish once the command has exited, and init has killed what it left
// behind.
//
// The connection stays open: init closes it once the host has
// acknowledged the status, which tells it the output was written.
func (o *Output) Finish() (int64, error) {
	<-o.done
	o.r.Close()
	return o.sent, o.err
}
