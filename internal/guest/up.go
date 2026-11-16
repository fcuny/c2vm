package guest

import (
	"errors"
	"fmt"
	"io"
)

// UpPort is the vsock port on the host that init connects to as soon as
// it starts. The kernel configures the network before it runs init, so
// the host learns from it that the VM got an address: a VM that doesn't
// connect in time most likely lost its network, and the host restarts
// it. The host answers with one byte, Up, for init to go on. It closes
// the connection without one when it gave up on the VM, which it's
// stopping: init then waits for that, so the command never runs in a VM
// the host replaces.
const UpPort = 1029

// Up is the host's answer on UpPort.
const Up = 'u'

// ErrHostGaveUp is the host closing UpPort's connection without
// answering: it gave up on the VM, and is stopping it.
var ErrHostGaveUp = errors.New("the host gave up on this vm")

// ReadUp reads the host's answer on UpPort. It returns nil for Up, and
// ErrHostGaveUp if the connection ends without an answer. Any other
// failure isn't the host giving up, which only closes the connection.
func ReadUp(r io.Reader) error {
	var buf [1]byte
	_, err := io.ReadFull(r, buf[:])
	switch {
	case errors.Is(err, io.EOF):
		return ErrHostGaveUp
	case err != nil:
		return fmt.Errorf("reading the host's answer: %w", err)
	case buf[0] != Up:
		return fmt.Errorf("the host answered %q, want %q", buf[0], Up)
	}
	return nil
}
