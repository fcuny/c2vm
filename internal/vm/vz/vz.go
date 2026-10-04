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

	if _, err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid VM configuration: %w", err)
	}
	return config, nil
}

// Run boots the VM, with its console attached to the process's stdio,
// and waits for it to stop. Interrupting c2vm stops the VM: there's
// nothing to lose, its changes only live in memory.
func (b *Backend) Run(ctx context.Context, spec vm.Spec) error {
	config, err := configuration(spec)
	if err != nil {
		return err
	}

	machine, err := vz.NewVirtualMachine(config)
	if err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := machine.Start(); err != nil {
		return fmt.Errorf("failed to start the vm: %w", err)
	}

	states := machine.StateChangedNotify()
	for {
		select {
		case state := <-states:
			switch state {
			case vz.VirtualMachineStateStopped:
				log.Print("vm stopped")
				return nil
			case vz.VirtualMachineStateError:
				return errors.New("the vm stopped with an error")
			}
		case <-signals:
			log.Print("stopping the vm")
			if err := machine.Stop(); err != nil {
				return fmt.Errorf("failed to stop the vm: %w", err)
			}
		case <-ctx.Done():
			if err := machine.Stop(); err != nil {
				return fmt.Errorf("failed to stop the vm: %w", err)
			}
			return ctx.Err()
		}
	}
}
