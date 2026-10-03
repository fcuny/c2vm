package vm

import (
	"strings"
	"testing"
)

func TestFirecrackerConfig(t *testing.T) {
	cfg := Config{
		SocketPath: "/run/c2vm/fc.sock",
		Kernel:     "/boot/vmlinux",
		Init:       "/sbin/init",
		RootDrive:  "/var/lib/c2vm/rootfs.img",
		CPUs:       2,
		MemoryMiB:  1024,
		CNINetwork: "c2vm",
	}.firecrackerConfig()

	if !strings.Contains(cfg.KernelArgs, " init=/sbin/init ") {
		t.Errorf("kernel args don't set init: %q", cfg.KernelArgs)
	}
	if got := *cfg.Drives[0].PathOnHost; got != "/var/lib/c2vm/rootfs.img" {
		t.Errorf("root drive is %q", got)
	}
	if !*cfg.Drives[0].IsRootDevice {
		t.Error("drive is not the root device")
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
