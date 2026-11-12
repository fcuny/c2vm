//go:build darwin

package vz

import (
	"strings"
	"testing"

	"fcuny.net/c2vm/internal/guest"
)

func TestShutdown(t *testing.T) {
	// A guest that reboots is restarted, so the VM would never stop.
	if got := New().Shutdown(); got != guest.ShutdownPowerOff {
		t.Errorf("Shutdown() = %q, want %q", got, guest.ShutdownPowerOff)
	}
}

func TestCommandLine(t *testing.T) {
	args := strings.Fields(commandLine)
	// A panic that rebooted the guest would restart the VM and run the
	// command again: the host stops it instead.
	// The kernel's messages, panics included, go to their own console.
	for _, want := range []string{"console=hvc1", "ip=dhcp", "oops=panic", "panic=0"} {
		found := false
		for _, arg := range args {
			found = found || arg == want
		}
		if !found {
			t.Errorf("command line %q is missing %s", commandLine, want)
		}
	}
	for _, arg := range args {
		if arg == "console="+strings.TrimPrefix(guest.CommandConsole, "/dev/") {
			t.Errorf("command line %q puts kernel messages on the command's console", commandLine)
		}
		if strings.HasPrefix(arg, "init=") || strings.HasPrefix(arg, "root=") {
			t.Errorf("command line %q sets %s, but the initramfs' init mounts the image", commandLine, arg)
		}
	}
}
