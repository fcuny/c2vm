package guest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ResizePort is the vsock port on the host that init gets the terminal's
// size from, when the command is interactive.
const ResizePort = 1025

// EncodeSize returns the message the host sends when its terminal's size
// changes: the rows and columns, in decimal, on a line.
func EncodeSize(rows, columns uint16) []byte {
	return fmt.Appendf(nil, "%d %d\n", rows, columns)
}

// ReadSizes reads the sizes the host sends, and calls resize with each,
// until r is closed.
func ReadSizes(r io.Reader, resize func(rows, columns uint16)) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		rows, columns, err := parseSize(scanner.Text())
		if err != nil {
			return err
		}
		resize(rows, columns)
	}
	return scanner.Err()
}

func parseSize(line string) (uint16, uint16, error) {
	r, c, ok := strings.Cut(line, " ")
	if !ok {
		return 0, 0, fmt.Errorf("invalid terminal size %q", line)
	}
	rows, err := strconv.ParseUint(r, 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid terminal size %q", line)
	}
	columns, err := strconv.ParseUint(c, 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid terminal size %q", line)
	}
	return uint16(rows), uint16(columns), nil
}
