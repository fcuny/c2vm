//go:build darwin

package vz

import (
	"strings"
	"testing"

	"fcuny.net/containerd-to-vm/internal/guest"
)

func TestShutdown(t *testing.T) {
	// A guest that reboots is restarted, so the VM would never stop.
	if got := New().Shutdown(); got != guest.ShutdownPowerOff {
		t.Errorf("Shutdown() = %q, want %q", got, guest.ShutdownPowerOff)
	}
}

func TestCommandLine(t *testing.T) {
	args := strings.Fields(commandLine)
	for _, want := range []string{"console=hvc0", "ip=dhcp"} {
		found := false
		for _, arg := range args {
			found = found || arg == want
		}
		if !found {
			t.Errorf("command line %q is missing %s", commandLine, want)
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "init=") || strings.HasPrefix(arg, "root=") {
			t.Errorf("command line %q sets %s, but the initramfs' init mounts the image", commandLine, arg)
		}
	}
}
