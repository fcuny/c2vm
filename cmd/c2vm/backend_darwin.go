//go:build darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"fcuny.net/c2vm/internal/vm"
	"fcuny.net/c2vm/internal/vm/vz"
)

// stuckBootsEnv is how many of the VM's first boots c2vm stops as if
// init hadn't started in time, which macOS losing the VM's network
// causes. It's for the e2e tests, to exercise the restart that follows.
const stuckBootsEnv = "C2VM_STUCK_BOOTS"

// backendFlags registers the backend's flags, and returns a function
// that creates the backend once they're parsed. Virtualization.framework
// has none.
func backendFlags(fs *flag.FlagSet) func() (vm.Backend, error) {
	return func() (vm.Backend, error) {
		b := vz.New()
		if v := os.Getenv(stuckBootsEnv); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%s=%q: expected a number of boots", stuckBootsEnv, v)
			}
			b.StuckBoots = n
		}
		return b, nil
	}
}
