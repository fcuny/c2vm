package guest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// StatusPort is the vsock port on the host that init reports the
// command's exit status to.
const StatusPort = 1024

// Status is what init reports once the command exits.
type Status struct {
	// Code is the command's exit status.
	Code int
	// Stdout and Stderr are how many bytes of the command's outputs init
	// sent the host, which writes them before it acknowledges the status.
	// The end of the connections can't tell it: Virtualization.framework
	// drops a guest's shutdown that arrives while it still holds data for
	// the host.
	Stdout, Stderr int64
}

// EncodeStatus returns the message init sends to report s: the exit
// status and the byte counts, in decimal, on a line.
func EncodeStatus(s Status) []byte {
	return fmt.Appendf(nil, "%d %d %d\n", s.Code, s.Stdout, s.Stderr)
}

// ReadStatus reads the status init sends. It reads a single line, and
// doesn't wait for the connection to be closed: init waits for the host
// to close it, to know the command's output was written before it stops
// the VM.
func ReadStatus(r io.Reader) (Status, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 64)).ReadString('\n')
	if err != nil {
		return Status{}, fmt.Errorf("reading the exit status: %w", err)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\n"), " ")
	invalid := fmt.Errorf("invalid exit status %q", line)
	if len(fields) != 3 {
		return Status{}, invalid
	}
	code, err := strconv.Atoi(fields[0])
	if err != nil || code < 0 || code > 255 {
		return Status{}, invalid
	}
	var counts [2]int64
	for i, f := range fields[1:] {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n < 0 {
			return Status{}, invalid
		}
		counts[i] = n
	}
	return Status{Code: code, Stdout: counts[0], Stderr: counts[1]}, nil
}
