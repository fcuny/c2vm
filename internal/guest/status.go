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

// EncodeStatus returns the message init sends to report the command's
// exit status: the status, in decimal, on a line.
func EncodeStatus(code int) []byte {
	return []byte(strconv.Itoa(code) + "\n")
}

// ReadStatus reads the exit status init sends. It reads a single line,
// and doesn't wait for the connection to be closed: init waits for the
// host to close it, to know the status was read before it stops the VM.
func ReadStatus(r io.Reader) (int, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 16)).ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("reading the exit status: %w", err)
	}
	code, err := strconv.Atoi(strings.TrimSuffix(line, "\n"))
	if err != nil || code < 0 || code > 255 {
		return 0, fmt.Errorf("invalid exit status %q", line)
	}
	return code, nil
}
