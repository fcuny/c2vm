// Package vm defines what c2vm needs from a hypervisor to run a VM.
package vm

import "context"

// Spec is the VM to run.
type Spec struct {
	// Kernel is the path to the kernel.
	Kernel string
	// Initrd is the path to the initramfs, which runs init.
	Initrd string
	// Image is the path to the ext4 image of the container's
	// filesystem. The VM sees it as /dev/vda and can't modify it.
	Image string
	// CPUs is the number of vCPUs.
	CPUs int64
	// MemoryMiB is the amount of memory, in MiB.
	MemoryMiB int64
	// TTY puts the terminal in raw mode, to pass every key to the
	// guest's console, for interactive commands.
	TTY bool
}

// Backend runs VMs with a given hypervisor.
type Backend interface {
	// Shutdown is how the guest has to stop for the hypervisor to stop
	// the VM: guest.ShutdownReboot or guest.ShutdownPowerOff.
	Shutdown() string
	// Run boots the VM, with its console attached to the process's
	// stdio, waits for it to stop, and returns the command's exit
	// status, which init reports before stopping the VM.
	Run(ctx context.Context, spec Spec) (int, error)
}
