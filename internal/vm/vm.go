// Package vm boots a root drive in a firecracker microVM.
package vm

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
)

// Config describes the VM to run.
type Config struct {
	// FirecrackerBinary is the path to the firecracker binary.
	FirecrackerBinary string
	// SocketPath is where firecracker creates its API socket.
	SocketPath string
	// Kernel is the path to an uncompressed linux kernel (vmlinux).
	Kernel string
	// Init is the path, inside the root drive, of the program the kernel
	// runs as PID 1.
	Init string
	// RootDrive is the path to the ext4 image used as the root drive.
	RootDrive string
	// MetricsFifo, if set, is a FIFO firecracker writes its metrics to.
	MetricsFifo string
	// CPUs is the number of vCPUs.
	CPUs int64
	// MemoryMiB is the amount of memory, in MiB.
	MemoryMiB int64
	// CNINetwork is the name of the CNI network the VM is attached to.
	CNINetwork string
}

func (c Config) firecrackerConfig() firecracker.Config {
	return firecracker.Config{
		LogLevel:        "debug",
		SocketPath:      c.SocketPath,
		KernelImagePath: c.Kernel,
		KernelArgs:      "console=ttyS0 reboot=k panic=1 acpi=off pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd init=" + c.Init + " random.trust_cpu=on",
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("1"),
				PathOnHost:   firecracker.String(c.RootDrive),
				IsRootDevice: firecracker.Bool(true),
				IsReadOnly:   firecracker.Bool(false),
			},
		},
		MetricsFifo: c.MetricsFifo,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(c.CPUs),
			Smt:        firecracker.Bool(true),
			MemSizeMib: firecracker.Int64(c.MemoryMiB),
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
func Run(ctx context.Context, c Config) error {
	vmmCtx, vmmCancel := context.WithCancel(ctx)
	defer vmmCancel()

	fcCfg := c.firecrackerConfig()

	command := firecracker.VMCommandBuilder{}.
		WithBin(c.FirecrackerBinary).
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
