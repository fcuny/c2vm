//go:build darwin

// Package vz runs VMs with Apple's Virtualization.framework.
package vz

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Code-Hex/vz/v3"

	"fcuny.net/c2vm/internal/guest"
	"fcuny.net/c2vm/internal/vm"
)

// commandLine is the kernel's command line. The console is the virtio
// console, and the network is configured with DHCP from
// Virtualization.framework's NAT, which also provides a nameserver.
// Kernel messages are left out so the console shows the command's
// output; c2vm-init reports the VM's address.
const commandLine = "console=hvc0 ip=dhcp quiet"

// Backend runs VMs with Virtualization.framework.
type Backend struct{}

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

func configuration(spec vm.Spec) (*vz.VirtualMachineConfiguration, error) {
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

	console, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	consoleConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(console)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleConfig})

	disk, err := vz.NewDiskImageStorageDeviceAttachment(spec.Image, true)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", spec.Image, err)
	}
	block, err := vz.NewVirtioBlockDeviceConfiguration(disk)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", spec.Image, err)
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{block})

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	network, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
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
// as with a shell.
func (b *Backend) Run(ctx context.Context, spec vm.Spec) (int, error) {
	config, err := configuration(spec)
	if err != nil {
		return 0, err
	}

	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return 0, err
	}

	statuses, err := listenStatus(machine)
	if err != nil {
		return 0, err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := machine.Start(); err != nil {
		return 0, fmt.Errorf("failed to start the vm: %w", err)
	}

	var interrupted syscall.Signal
	states := machine.StateChangedNotify()
	for {
		select {
		case state := <-states:
			switch state {
			case vz.VirtualMachineStateStopped:
				log.Print("vm stopped")
				// init waits for the status to be read before it stops
				// the VM, so it's there if it was sent.
				select {
				case code := <-statuses:
					return code, nil
				default:
				}
				if interrupted != 0 {
					return 128 + int(interrupted), nil
				}
				return 0, errors.New("the vm stopped without reporting the command's exit status")
			case vz.VirtualMachineStateError:
				return 0, errors.New("the vm stopped with an error")
			}
		case sig := <-signals:
			log.Print("stopping the vm")
			interrupted = sig.(syscall.Signal)
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

// listenStatus listens for init to report the command's exit status,
// and sends it on the returned channel.
func listenStatus(machine *vz.VirtualMachine) (<-chan int, error) {
	devices := machine.SocketDevices()
	if len(devices) != 1 {
		return nil, fmt.Errorf("expected one vsock device, got %d", len(devices))
	}
	listener, err := devices[0].Listen(guest.StatusPort)
	if err != nil {
		return nil, fmt.Errorf("vsock: %w", err)
	}

	statuses := make(chan int, 1)
	go func() {
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("waiting for the exit status: %v", err)
			return
		}
		// Closing the connection tells init the status was read.
		defer conn.Close()
		code, err := guest.ReadStatus(conn)
		if err != nil {
			log.Print(err)
			return
		}
		statuses <- code
	}()
	return statuses, nil
}
