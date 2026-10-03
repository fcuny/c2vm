//go:build !linux

package main

import (
	"flag"
	"fmt"
	"runtime"

	"fcuny.net/containerd-to-vm/internal/vm"
)

// backendFlags registers the backend's flags, and returns a function
// that creates the backend once they're parsed. There's no backend for
// this platform yet.
func backendFlags(fs *flag.FlagSet) func() (vm.Backend, error) {
	return func() (vm.Backend, error) {
		return nil, fmt.Errorf("booting VMs isn't supported on %s yet", runtime.GOOS)
	}
}
