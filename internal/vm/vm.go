// Package vm defines what c2vm needs from a hypervisor to run a VM.
package vm

import (
	"context"
	"net"
)

// Spec is the VM to run.
type Spec struct {
	// Kernel is the path to the kernel.
	Kernel string
	// Initrd is the path to the initramfs, which runs init.
	Initrd string
	// Image is the path to the ext4 image of the container's
	// filesystem. The VM sees it as /dev/vda and can't modify it.
	Image string
	// Scratch is the path to a disk image for the overlay's writable
	// layer, when set. The VM sees it as ScratchDevice, and writes to it.
	// The hypervisor keeps it open while the VM runs, so it can be
	// removed once Started is called.
	Scratch string
	// Started, when set, is called once the VM has started for good:
	// init is running, and the backend won't restart the VM, which it
	// may do before then, with the same paths.
	Started func()
	// CPUs is the number of vCPUs.
	CPUs int64
	// MemoryMiB is the amount of memory, in MiB.
	MemoryMiB int64
	// TTY puts the terminal in raw mode, to pass every key to the
	// guest's console, for interactive commands.
	TTY bool
	// Stdin passes the process's stdin to init, for the command to
	// read, when TTY isn't set.
	Stdin bool
	// Shares are the host's directories to share with the VM.
	Shares []Share
	// MAC is the MAC address of the VM's network interface. The
	// backend picks a random one when it's nil.
	MAC net.HardwareAddr
}

// ScratchDevice is the scratch disk's device in the VM: the drive after
// the image.
const ScratchDevice = "/dev/vdb"

// Share is a directory of the host the VM can mount.
type Share struct {
	// Path is the directory, on the host.
	Path string
	// Tag is the name the VM mounts it by.
	Tag string
	// ReadOnly keeps the VM from modifying it.
	ReadOnly bool
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
