//go:build linux

// Package firecracker runs VMs with firecracker.
package firecracker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"

	"fcuny.net/containerd-to-vm/internal/guest"
	"fcuny.net/containerd-to-vm/internal/vm"
)

// Config is how to run firecracker.
type Config struct {
	// Binary is the path to the firecracker binary.
	Binary string
	// SocketPath is where firecracker creates its API socket. When empty,
	// it's created in a temporary directory.
	SocketPath string
	// MetricsFifo, if set, is a FIFO firecracker writes its metrics to.
	MetricsFifo string
	// CNINetwork is the name of the CNI network the VM is attached to.
	CNINetwork string
}

// Backend runs VMs with firecracker.
type Backend struct {
	config Config
}

var _ vm.Backend = (*Backend)(nil)

// New returns a backend that runs VMs with firecracker.
func New(config Config) *Backend {
	return &Backend{config: config}
}

// Shutdown is guest.ShutdownReboot: with reboot=k on the kernel command
// line, firecracker exits when the guest reboots.
func (b *Backend) Shutdown() string {
	return guest.ShutdownReboot
}

func firecrackerConfig(c Config, spec vm.Spec, socketPath string) firecracker.Config {
	return firecracker.Config{
		LogLevel:        "debug",
		SocketPath:      socketPath,
		KernelImagePath: spec.Kernel,
		InitrdPath:      spec.Initrd,
		KernelArgs:      "console=ttyS0 reboot=k panic=1 acpi=off pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd random.trust_cpu=on",
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("image"),
				PathOnHost:   firecracker.String(spec.Image),
				IsRootDevice: firecracker.Bool(false),
				IsReadOnly:   firecracker.Bool(true),
			},
		},
		MetricsFifo: c.MetricsFifo,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(spec.CPUs),
			Smt:        firecracker.Bool(true),
			MemSizeMib: firecracker.Int64(spec.MemoryMiB),
		},
		NetworkInterfaces: []firecracker.NetworkInterface{
			{
				CNIConfiguration: &firecracker.CNIConfiguration{
					NetworkName: c.CNINetwork,
					IfName:      "eth0",
				},
			},
		},
	}
}

// Run boots the VM, with its console attached to the process's stdio,
// and waits for it to exit.
func (b *Backend) Run(ctx context.Context, spec vm.Spec) error {
	vmmCtx, vmmCancel := context.WithCancel(ctx)
	defer vmmCancel()

	socketPath := b.config.SocketPath
	if socketPath == "" {
		dir, err := os.MkdirTemp("", "c2vm-firecracker")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		socketPath = filepath.Join(dir, "firecracker.sock")
	}

	fcCfg := firecrackerConfig(b.config, spec, socketPath)

	command := firecracker.VMCommandBuilder{}.
		WithBin(b.config.Binary).
		WithSocketPath(fcCfg.SocketPath).
		WithStdin(os.Stdin).
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		Build(ctx)

	m, err := firecracker.NewMachine(vmmCtx, fcCfg, firecracker.WithProcessRunner(command))
	if err != nil {
		return fmt.Errorf("failed to create the vm: %w", err)
	}

	if err := m.Start(vmmCtx); err != nil {
		return fmt.Errorf("failed to start the vm: %w", err)
	}
	defer m.StopVMM()

	if err := m.Wait(vmmCtx); err != nil {
		return fmt.Errorf("vm exited with an error: %w", err)
	}
	log.Print("vm exited")
	return nil
}
