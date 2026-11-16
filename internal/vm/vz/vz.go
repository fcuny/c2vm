//go:build darwin

// Package vz runs VMs with Apple's Virtualization.framework.
package vz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v4"
	"golang.org/x/sys/unix"

	"fcuny.net/c2vm/internal/guest"
	"fcuny.net/c2vm/internal/vm"
)

// commandLine is the kernel's command line. The kernel's console is the
// second virtio console, hvc1 (see guest.CommandConsole), and the
// network is configured with DHCP from Virtualization.framework's NAT,
// which also provides a nameserver. Kernel messages are left out, but
// for errors; c2vm-init reports the VM's address. An oops panics the
// kernel, and a panic leaves it waiting, whatever its configuration
// says, for the host to see the panic on the console and stop the VM.
const commandLine = "console=hvc1 ip=dhcp quiet oops=panic panic=0"

// bootDeadline is how long init has to report it started (see
// guest.UpPort) once the VM has. Before init runs, the kernel configures
// the network with DHCP: booting takes well under 2s. A VM that misses the deadline most
// likely lost its network to a race in macOS's NAT: when the last VM on
// it stops while another VM's network interface is being attached,
// macOS stops the NAT, and never attaches that interface, so DHCP never
// completes. Nothing of the command has run yet, and a new VM gets a new
// interface: Run restarts the VM.
const bootDeadline = 10 * time.Second

// maxBoots is how many times Run starts a VM whose init misses
// bootDeadline before it gives up.
const maxBoots = 3

// errBootStuck is a boot whose init missed bootDeadline.
var errBootStuck = errors.New("init didn't connect to the host in time")

// Backend runs VMs with Virtualization.framework.
type Backend struct {
	// StuckBoots is how many of each VM's first boots to stop as if
	// their init had missed bootDeadline. The race that makes a VM lose
	// its network is too rare for the e2e tests to count on: this has
	// them exercise the restart instead.
	StuckBoots int
}

var _ vm.Backend = (*Backend)(nil)

// New returns a backend that runs VMs with Virtualization.framework.
func New() *Backend {
	return &Backend{}
}

// Shutdown is guest.ShutdownPowerOff: Virtualization.framework restarts
// a guest that reboots, and stops one that powers off.
func (b *Backend) Shutdown() string {
	return guest.ShutdownPowerOff
}

// serialPort is what a console reads its input from, and writes its
// output to.
type serialPort struct{ input, output *os.File }

// configuration returns the VM's configuration, with the command's
// console, hvc0, and the kernel's, hvc1.
func configuration(spec vm.Spec, command, kernel serialPort) (*vz.VirtualMachineConfiguration, error) {
	bootLoader, err := vz.NewLinuxBootLoader(spec.Kernel,
		vz.WithCommandLine(commandLine),
		vz.WithInitrd(spec.Initrd),
	)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, uint(spec.CPUs), uint64(spec.MemoryMiB)*1024*1024)
	if err != nil {
		return nil, err
	}

	// The guest names the consoles in this order.
	var consoles []*vz.VirtioConsoleDeviceSerialPortConfiguration
	for _, port := range []serialPort{command, kernel} {
		attachment, err := vz.NewFileHandleSerialPortAttachment(port.input, port.output)
		if err != nil {
			return nil, fmt.Errorf("console: %w", err)
		}
		console, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
		if err != nil {
			return nil, fmt.Errorf("console: %w", err)
		}
		consoles = append(consoles, console)
	}
	config.SetSerialPortsVirtualMachineConfiguration(consoles)

	disk, err := vz.NewDiskImageStorageDeviceAttachment(spec.Image, true)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", spec.Image, err)
	}
	block, err := vz.NewVirtioBlockDeviceConfiguration(disk)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", spec.Image, err)
	}
	// The guest names the disks in this order: the image is /dev/vda,
	// and the scratch disk vm.ScratchDevice.
	disks := []vz.StorageDeviceConfiguration{block}
	if spec.Scratch != "" {
		// Its contents are thrown away with the VM, so the host never
		// has to flush them to storage when the guest asks it to.
		scratch, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(spec.Scratch, false,
			vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeNone)
		if err != nil {
			return nil, fmt.Errorf("scratch disk %s: %w", spec.Scratch, err)
		}
		block, err := vz.NewVirtioBlockDeviceConfiguration(scratch)
		if err != nil {
			return nil, fmt.Errorf("scratch disk %s: %w", spec.Scratch, err)
		}
		disks = append(disks, block)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(disks)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	network, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	var mac *vz.MACAddress
	if spec.MAC != nil {
		mac, err = vz.NewMACAddress(spec.MAC)
	} else {
		mac, err = vz.NewRandomLocallyAdministeredMACAddress()
	}
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	network.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{network})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// Each share is its own virtiofs device, which init mounts by its
	// tag.
	var shares []vz.DirectorySharingDeviceConfiguration
	for _, s := range spec.Shares {
		dir, err := vz.NewSharedDirectory(s.Path, s.ReadOnly)
		if err != nil {
			return nil, fmt.Errorf("sharing %s: %w", s.Path, err)
		}
		share, err := vz.NewSingleDirectoryShare(dir)
		if err != nil {
			return nil, fmt.Errorf("sharing %s: %w", s.Path, err)
		}
		fs, err := vz.NewVirtioFileSystemDeviceConfiguration(s.Tag)
		if err != nil {
			return nil, fmt.Errorf("sharing %s: %w", s.Path, err)
		}
		fs.SetDirectoryShare(share)
		shares = append(shares, fs)
	}
	config.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)

	// init reports the command's exit status over vsock.
	socket, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("vsock: %w", err)
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socket})

	if _, err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid VM configuration: %w", err)
	}
	return config, nil
}

// Run boots the VM, with its console attached to the process's stdio,
// waits for it to stop, and returns the command's exit status.
// Interrupting c2vm stops the VM: there's nothing to lose, its changes
// only live in memory. The status is then 128 plus the signal's number,
// as with a shell. With spec.TTY, the terminal is in raw mode, so Ctrl-C
// goes to the guest; escapeKey stops the VM instead, as SIGINT would.
// Otherwise, the command's stdout and stderr come over vsock, each to
// its own, and with spec.Stdin, stdin goes to the command over vsock.
// Run fails if either arrives cut short. A VM whose init doesn't start
// within bootDeadline is restarted, up to maxBoots times in all.
func (b *Backend) Run(ctx context.Context, spec vm.Spec) (int, error) {
	// The kernel's console, the command's stderr and c2vm's messages all
	// write to stderr.
	stderr := &lockedWriter{w: waitWriter{int(os.Stderr.Fd())}}
	log.SetOutput(stderr)
	defer log.SetOutput(os.Stderr)
	s := &session{spec: spec, stderr: stderr}

	if spec.TTY {
		stdin, err := nonblockingInput(os.Stdin)
		if err != nil {
			return 0, err
		}
		defer stdin.Close()
		r, esc, err := forwardInput(stdin)
		if err != nil {
			return 0, err
		}
		// The console reads from r for as long as the VM runs.
		defer r.Close()
		s.input, s.escaped = r, esc
		s.consoleOutput = os.Stdout
	} else {
		// The command doesn't use its console: give it a pipe that's
		// never written to, so it doesn't take any of stdin, and
		// discard what's written to it, so it stays out of stdout.
		r, w, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		defer r.Close()
		defer w.Close()
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		defer devNull.Close()
		s.input, s.consoleOutput = r, devNull
	}

	// The kernel's console goes to stderr, with c2vm's messages.
	console, err := watchConsole(stderr)
	if err != nil {
		return 0, err
	}
	// Once the VM has stopped, or failed to start.
	defer console.drain()
	s.console = console

	if spec.Stdin && !spec.TTY {
		stdin, err := nonblockingInput(os.Stdin)
		if err != nil {
			return 0, err
		}
		defer stdin.Close()
		// nonblockingInput changes stdin's mode, which whatever reads
		// it after c2vm doesn't expect.
		defer unix.SetNonblock(int(os.Stdin.Fd()), false)
		s.stdin = stdin
	}

	s.signals = make(chan os.Signal, 1)
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(s.signals)
	defer s.restoreTerminal()

	return boots(maxBoots, func(n int) (int, error) {
		// Interrupted while the last boot stopped.
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		deadline := bootDeadline
		if n < b.StuckBoots {
			deadline = 0
		}
		return s.boot(ctx, deadline)
	})
}

// boots calls boot until a boot's init doesn't miss bootDeadline, up to
// max times. n counts the boots from 0.
func boots(max int, boot func(n int) (int, error)) (int, error) {
	for n := 0; ; n++ {
		code, err := boot(n)
		if !errors.Is(err, errBootStuck) {
			return code, err
		}
		if n+1 >= max {
			return 0, fmt.Errorf("the vm's init didn't start within %v, %d times in a row, most likely because it got no network address (see Network in c2vm's README)", bootDeadline, max)
		}
		log.Printf("the vm's init didn't start within %v, most likely because macOS failed to connect its network: restarting the vm", bootDeadline)
	}
}

// session is what Run keeps across the VM's boots: the console, the
// command's stdin, and the terminal.
type session struct {
	spec   vm.Spec
	stderr io.Writer
	// input and consoleOutput are the command's console, hvc0.
	input, consoleOutput *os.File
	console              *console
	// stdin is passed to the command over vsock, with spec.Stdin and
	// without spec.TTY.
	stdin    *os.File
	escaped  <-chan struct{}
	signals  chan os.Signal
	terminal *terminal
}

// rawTerminal puts the terminal in raw mode, on the first boot.
func (s *session) rawTerminal() error {
	if s.terminal != nil {
		return nil
	}
	log.Print("press Ctrl-] to stop the vm")
	t, err := rawTerminal(os.Stdin, s.stderr)
	if err != nil {
		return fmt.Errorf("setting the terminal in raw mode: %w", err)
	}
	s.terminal = t
	return nil
}

func (s *session) restoreTerminal() {
	if s.terminal != nil {
		s.terminal.restore()
	}
}

// boot starts a new VM, waits for it to stop, and returns the command's
// exit status. It stops the VM and returns errBootStuck if init hasn't
// connected to the host within deadline.
func (s *session) boot(ctx context.Context, deadline time.Duration) (int, error) {
	config, err := configuration(s.spec, serialPort{s.input, s.consoleOutput}, s.console.port)
	if err != nil {
		return 0, err
	}

	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return 0, err
	}
	host := newVsockHost(machine)
	defer host.close()

	// Without -t, the command's outputs come over vsock. On a terminal,
	// stdout and stderr may share a file description with stdin, which
	// nonblockingInput makes non-blocking.
	var stdoutCopy, stderrCopy *output
	if !s.spec.TTY {
		stdoutCopy, err = receiveOutput(host, guest.StdoutPort, "stdout", waitWriter{int(os.Stdout.Fd())})
		if err != nil {
			return 0, err
		}
		stderrCopy, err = receiveOutput(host, guest.StderrPort, "stderr", s.stderr)
		if err != nil {
			return 0, err
		}
	}

	statuses, err := listenStatus(host, stdoutCopy, stderrCopy)
	if err != nil {
		return 0, err
	}

	if err := host.listenUp(); err != nil {
		return 0, err
	}

	if s.stdin != nil {
		if err := sendStdin(host, s.stdin); err != nil {
			return 0, err
		}
	}

	if s.spec.TTY {
		stop, err := sendResizes(host, int(os.Stdin.Fd()))
		if err != nil {
			return 0, err
		}
		defer stop()
		if err := s.rawTerminal(); err != nil {
			return 0, err
		}
	}

	// A notification only tells the state changed: vz drops one the
	// channel has no room for, as while the loop waits for Stop, and
	// replays none from before this, so it starts before the VM does.
	states := make(chan vz.VirtualMachineState, 1)
	stopStates, err := machine.StateChangedNotify(states)
	if err != nil {
		return 0, fmt.Errorf("watching the vm's state: %w", err)
	}
	defer stopStates()

	if err := machine.Start(); err != nil {
		return 0, fmt.Errorf("failed to start the vm: %w", err)
	}
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	late := timer.C
	connected := host.connected

	var interrupted syscall.Signal
	var panicked, stuck bool
	panics := s.console.panicked
	escaped := s.escaped
	for {
		select {
		case <-states:
			switch machine.State() {
			case vz.VirtualMachineStateStopped:
				log.Print("vm stopped")
				// init waits for the status to be read before it stops
				// the VM, so it's there if it was sent. When c2vm stopped
				// the VM itself, while the output was still being written,
				// the output is cut short because of that.
				select {
				case s := <-statuses:
					if s.err == nil || (!panicked && interrupted == 0) {
						return s.code, s.err
					}
				default:
				}
				if panicked {
					return 0, errors.New("the guest kernel panicked")
				}
				if interrupted != 0 {
					return 128 + int(interrupted), nil
				}
				if stuck {
					return 0, errBootStuck
				}
				return 0, errors.New("the vm stopped without reporting the command's exit status")
			case vz.VirtualMachineStateError:
				return 0, errors.New("the vm stopped with an error")
			}
		case <-connected:
			// init is up: the network came up with it, and the VM won't
			// be restarted.
			connected, late = nil, nil
			if s.spec.Started != nil {
				s.spec.Started()
			}
		case <-late:
			late = nil
			// The VM is already stopping, for another reason.
			if panicked || interrupted != 0 {
				continue
			}
			// It can't be stopped yet: try again shortly.
			if !machine.CanStop() {
				timer.Reset(100 * time.Millisecond)
				late = timer.C
				continue
			}
			// init may have connected since the deadline: then the boot
			// goes on. Otherwise, nothing it connects to from now on is
			// accepted, so this boot can't take what the next one needs.
			if !host.abandon() {
				continue
			}
			stuck = true
			if err := machine.Stop(); err != nil {
				return 0, fmt.Errorf("failed to stop the vm: %w", err)
			}
		case <-panics:
			// The channel is closed: stop waiting on it.
			panics = nil
			panicked = true
			// The VM may be stopping already, with the status sent.
			if !machine.CanStop() {
				continue
			}
			log.Print("the guest kernel panicked, stopping the vm")
			if err := machine.Stop(); err != nil {
				return 0, fmt.Errorf("failed to stop the vm: %w", err)
			}
		case sig := <-s.signals:
			log.Print("stopping the vm")
			interrupted = sig.(syscall.Signal)
			if err := machine.Stop(); err != nil {
				return 0, fmt.Errorf("failed to stop the vm: %w", err)
			}
		case <-escaped:
			// The channel is closed: stop waiting on it.
			escaped = nil
			log.Print("stopping the vm")
			interrupted = syscall.SIGINT
			if err := machine.Stop(); err != nil {
				return 0, fmt.Errorf("failed to stop the vm: %w", err)
			}
		case <-ctx.Done():
			if err := machine.Stop(); err != nil {
				return 0, fmt.Errorf("failed to stop the vm: %w", err)
			}
			return 0, ctx.Err()
		}
	}
}

// status is the command's exit status, or why c2vm fails though the
// command exited: its output was cut short, as docker run fails when
// the stream of the container's output does.
type status struct {
	code int
	err  error
}

// listenStatus listens for init to report the command's exit status,
// and sends it on the returned channel once the command's outputs have
// been written, if they come over vsock: stdout and stderr are nil with
// -t.
func listenStatus(host *vsockHost, stdout, stderr *output) (<-chan status, error) {
	listener, err := host.listen(guest.StatusPort)
	if err != nil {
		return nil, err
	}

	statuses := make(chan status, 1)
	go func() {
		defer listener.Close()
		conn, err := host.accept(listener)
		if err != nil {
			if !host.closedEarly() {
				log.Printf("waiting for the exit status: %v", err)
			}
			return
		}
		// Closing the connection tells init the status was read, and the
		// output written: it then stops the VM.
		defer conn.Close()
		reported, err := guest.ReadStatus(conn)
		if err != nil {
			log.Print(err)
			return
		}
		var errs []error
		for _, o := range []struct {
			output *output
			sent   int64
		}{{stdout, reported.Stdout}, {stderr, reported.Stderr}} {
			if o.output == nil {
				continue
			}
			errs = append(errs, o.output.wait(o.sent))
		}
		statuses <- status{code: reported.Code, err: errors.Join(errs...)}
	}()
	return statuses, nil
}

// sendStdin copies in to init once it connects, and closes the
// connection when in ends, for the command to read EOF. The command may
// exit without reading all of it, as with a pipe: that isn't an error.
func sendStdin(host *vsockHost, in io.Reader) error {
	listener, err := host.listen(guest.StdinPort)
	if err != nil {
		return err
	}

	go func() {
		defer listener.Close()
		conn, err := host.accept(listener)
		if err != nil {
			if !host.closedEarly() {
				log.Printf("waiting to pass stdin: %v", err)
			}
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, in)
	}()
	return nil
}
