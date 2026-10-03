//go:build linux

package main

import (
	"flag"
	"fmt"
	"os/exec"

	"fcuny.net/containerd-to-vm/internal/vm"
	"fcuny.net/containerd-to-vm/internal/vm/firecracker"
)

// backendFlags registers the backend's flags, and returns a function
// that creates the backend once they're parsed.
func backendFlags(fs *flag.FlagSet) func() (vm.Backend, error) {
	var config firecracker.Config
	fs.StringVar(&config.Binary, "firecracker-binary", "", "Path to the firecracker binary (default: firecracker on PATH)")
	fs.StringVar(&config.MetricsFifo, "metrics-fifo", "", "FIFO to the firecracker metrics")
	fs.StringVar(&config.SocketPath, "socket", "", "Path for firecracker's API socket (default: in a temporary directory)")
	fs.StringVar(&config.CNINetwork, "cni-network", "c2vm", "Name of the CNI network to attach the VM to")

	return func() (vm.Backend, error) {
		if config.Binary == "" {
			path, err := exec.LookPath("firecracker")
			if err != nil {
				return nil, fmt.Errorf("firecracker not found on PATH, set -firecracker-binary: %w", err)
			}
			config.Binary = path
		}
		return firecracker.New(config), nil
	}
}
