//go:build !darwin

package main

import (
	"flag"
	"fmt"
	"runtime"

	"fcuny.net/c2vm/internal/vm"
)

// backendFlags registers the backend's flags, and returns a function
// that creates the backend once they're parsed. VMs only boot on macOS,
// with Virtualization.framework.
func backendFlags(fs *flag.FlagSet) func() (vm.Backend, error) {
	return func() (vm.Backend, error) {
		return nil, fmt.Errorf("booting VMs is only supported on macOS, not %s", runtime.GOOS)
	}
}
