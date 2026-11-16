//go:build darwin

package vz

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/Code-Hex/vz/v4"

	"fcuny.net/c2vm/internal/guest"
)

// vsockHost is the host's side of one boot's vsock connections. It
// reports init's first connection, on guest.UpPort as soon as init
// starts, which tells the VM came up with its network. Once the boot is
// over, it closes the listeners init didn't connect to, so that a VM
// that's restarted can't take what's meant for the next one, such as
// stdin.
type vsockHost struct {
	machine *vz.VirtualMachine
	// connected is closed once init has connected to any port: up is
	// then set.
	connected chan struct{}

	mu        sync.Mutex
	listeners []*vz.VirtioSocketListener
	closed    bool
	// abandoned is set once the host gave up on init connecting: a
	// connection that comes after is refused, so a boot that's being
	// restarted can't take stdin.
	abandoned bool
	up        bool
}

func newVsockHost(machine *vz.VirtualMachine) *vsockHost {
	return &vsockHost{machine: machine, connected: make(chan struct{})}
}

// listen listens on port of the VM's vsock device, for connections from
// the guest.
func (h *vsockHost) listen(port uint32) (*vz.VirtioSocketListener, error) {
	devices := h.machine.SocketDevices()
	if len(devices) != 1 {
		return nil, fmt.Errorf("expected one vsock device, got %d", len(devices))
	}
	listener, err := devices[0].Listen(port)
	if err != nil {
		return nil, fmt.Errorf("vsock: listening on port %d: %w", port, err)
	}
	h.mu.Lock()
	h.listeners = append(h.listeners, listener)
	h.mu.Unlock()
	return listener, nil
}

// accept waits for init to connect to listener. It fails once close is
// called, or after abandon: closedEarly then tells that wasn't a problem
// worth reporting.
func (h *vsockHost) accept(listener interface{ Accept() (net.Conn, error) }) (net.Conn, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.abandoned {
		conn.Close()
		return nil, errAbandoned
	}
	h.markUp()
	return conn, nil
}

// markUp records that init connected. h.mu must be held.
func (h *vsockHost) markUp() {
	if !h.up {
		h.up = true
		close(h.connected)
	}
}

// errAbandoned is a connection that came after abandon.
var errAbandoned = errors.New("the host gave up on this boot")

// listenUp listens for init to report it started, and answers it (see
// guest.UpPort), until the boot is over: a guest that reboots starts
// init again.
func (h *vsockHost) listenUp() error {
	listener, err := h.listen(guest.UpPort)
	if err != nil {
		return err
	}
	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			h.answerUp(conn)
			conn.Close()
		}
	}()
	return nil
}

// answerUp lets init go on, unless the host gave up on the boot: it then
// closes conn without an answer, and init waits to be stopped. The boot
// is up only once init has the answer, so that a failed write leaves the
// deadline to restart it, rather than leave init waiting for an answer
// that never comes.
func (h *vsockHost) answerUp(conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.abandoned {
		return
	}
	if _, err := conn.Write([]byte{guest.Up}); err != nil {
		log.Printf("answering init: %v", err)
		return
	}
	h.markUp()
}

// abandon gives up on init connecting, unless it already has, and
// reports whether it gave up. Once it has, accept refuses every
// connection.
func (h *vsockHost) abandon() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.up {
		return false
	}
	h.abandoned = true
	return true
}

// closedEarly reports whether close or abandon was called: a pending
// accept then fails because the boot is over, rather than because of
// the guest.
func (h *vsockHost) closedEarly() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed || h.abandoned
}

// close stops listening on every port. Connections already accepted
// stay open.
func (h *vsockHost) close() {
	h.mu.Lock()
	h.closed = true
	listeners := h.listeners
	h.mu.Unlock()
	for _, l := range listeners {
		l.Close()
	}
}
