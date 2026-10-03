//go:build linux

package firecracker

import (
	"strings"
	"testing"

	"fcuny.net/containerd-to-vm/internal/guest"
	"fcuny.net/containerd-to-vm/internal/vm"
)

func TestFirecrackerConfig(t *testing.T) {
	cfg := firecrackerConfig(
		Config{CNINetwork: "c2vm"},
		vm.Spec{
			Kernel:    "/boot/vmlinux",
			Initrd:    "/var/lib/c2vm/initrd.cpio",
			Image:     "/var/lib/c2vm/rootfs.img",
			CPUs:      2,
			MemoryMiB: 1024,
		},
		"/run/c2vm/fc.sock",
	)

	if strings.Contains(cfg.KernelArgs, "init=") {
		t.Errorf("kernel args override init, the initramfs' should run: %q", cfg.KernelArgs)
	}
	if cfg.InitrdPath != "/var/lib/c2vm/initrd.cpio" {
		t.Errorf("initrd is %q", cfg.InitrdPath)
	}
	if got := *cfg.Drives[0].PathOnHost; got != "/var/lib/c2vm/rootfs.img" {
		t.Errorf("image drive is %q", got)
	}
	if *cfg.Drives[0].IsRootDevice || !*cfg.Drives[0].IsReadOnly {
		t.Error("the image should be a read-only data drive: init mounts it")
	}
	if got := *cfg.MachineCfg.VcpuCount; got != 2 {
		t.Errorf("vcpus = %d, want 2", got)
	}
	if got := *cfg.MachineCfg.MemSizeMib; got != 1024 {
		t.Errorf("memory = %d, want 1024", got)
	}
	if got := cfg.NetworkInterfaces[0].CNIConfiguration.NetworkName; got != "c2vm" {
		t.Errorf("CNI network = %q, want c2vm", got)
	}
}

func TestShutdown(t *testing.T) {
	if got := New(Config{}).Shutdown(); got != guest.ShutdownReboot {
		t.Errorf("Shutdown() = %q, want %q", got, guest.ShutdownReboot)
	}
}
