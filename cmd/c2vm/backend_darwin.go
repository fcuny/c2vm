//go:build darwin

package main

import (
	"flag"

	"fcuny.net/containerd-to-vm/internal/vm"
	"fcuny.net/containerd-to-vm/internal/vm/vz"
)

// backendFlags registers the backend's flags, and returns a function
// that creates the backend once they're parsed. Virtualization.framework
// has none.
func backendFlags(fs *flag.FlagSet) func() (vm.Backend, error) {
	return func() (vm.Backend, error) {
		return vz.New(), nil
	}
}
