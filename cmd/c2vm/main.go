// Command c2vm boots a container image as a firecracker microVM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"fcuny.net/containerd-to-vm/internal/guest"
	"fcuny.net/containerd-to-vm/internal/image"
	"fcuny.net/containerd-to-vm/internal/initramfs"
	"fcuny.net/containerd-to-vm/internal/rootfs"
	"fcuny.net/containerd-to-vm/internal/vm"
)

const defaultNamespace = "c2vm"

type options struct {
	container         string
	outFile           string
	kernel            string
	firecrackerBinary string
	initBinary        string
	metricsFifo       string
	containerdSocket  string
	socketPath        string
	cniNetwork        string
	size              string
	cpus              int64
	memoryMiB         int64
	platform          ocispec.Platform
}

func parseFlags(args []string) (options, error) {
	var (
		opts     options
		platform string
	)

	fs := flag.NewFlagSet("c2vm", flag.ContinueOnError)
	fs.StringVar(&opts.container, "container", "", "Image to boot, as a fully qualified reference (e.g. docker.io/library/alpine:latest)")
	fs.StringVar(&opts.outFile, "out", "container.img", "Path to store the image")
	fs.StringVar(&opts.kernel, "kernel", "", "Path to the linux kernel image")
	fs.StringVar(&opts.firecrackerBinary, "firecracker-binary", "", "Path to the firecracker binary")
	fs.StringVar(&opts.initBinary, "init", "", "Path to the c2vm-init binary (default: c2vm-init next to c2vm)")
	fs.StringVar(&opts.metricsFifo, "metrics-fifo", "", "FIFO to the firecracker metrics")
	fs.StringVar(&opts.containerdSocket, "containerd", "/run/containerd/containerd.sock", "Path to containerd's socket")
	fs.StringVar(&opts.socketPath, "socket", "", "Path for firecracker's API socket (default: in a temporary directory)")
	fs.StringVar(&opts.cniNetwork, "cni-network", "c2vm", "Name of the CNI network to attach the VM to")
	fs.StringVar(&opts.size, "size", "2G", "Size of the image before it's shrunk to fit, as understood by fallocate")
	fs.Int64Var(&opts.cpus, "cpus", 1, "Number of vCPUs")
	fs.Int64Var(&opts.memoryMiB, "memory", 512, "Memory for the VM, in MiB")
	fs.StringVar(&platform, "platform", "linux/"+runtime.GOARCH, "Platform of the image to pull")

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}

	if opts.container == "" {
		return options{}, errors.New("a container is required")
	}
	if opts.kernel == "" {
		return options{}, errors.New("a linux kernel is required")
	}
	if opts.firecrackerBinary == "" {
		return options{}, errors.New("the path to the firecracker binary is required")
	}
	if opts.cpus < 1 {
		return options{}, errors.New("-cpus must be at least 1")
	}
	if opts.memoryMiB < 1 {
		return options{}, errors.New("-memory must be at least 1")
	}

	p, err := platforms.Parse(platform)
	if err != nil {
		return options{}, fmt.Errorf("invalid -platform: %w", err)
	}
	if p.OS != "linux" {
		return options{}, fmt.Errorf("invalid -platform %q: only linux images can be booted", platform)
	}
	opts.platform = p

	return opts, nil
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}

	if err := run(opts); err != nil {
		log.Fatal(err)
	}
}

func run(opts options) error {
	initBinary, err := findInit(opts.initBinary)
	if err != nil {
		return err
	}

	c, err := client.New(opts.containerdSocket)
	if err != nil {
		return fmt.Errorf("failed to create a client for containerd: %w", err)
	}
	defer c.Close()

	ctx := namespaces.WithNamespace(context.Background(), defaultNamespace)
	ctx, done, err := c.WithLease(ctx)
	if err != nil {
		return fmt.Errorf("failed to get a lease: %w", err)
	}
	defer done(ctx)

	img, err := image.Pull(ctx, c, opts.container, platforms.Only(opts.platform))
	if err != nil {
		return err
	}

	if err := buildRootfs(ctx, img, opts.outFile, opts.size); err != nil {
		return err
	}

	if err := rootfs.Shrink(opts.outFile); err != nil {
		return fmt.Errorf("failed to resize the image %s: %w", opts.outFile, err)
	}

	imageConfig, err := img.Config(ctx)
	if err != nil {
		return err
	}
	config, err := guest.FromImage(imageConfig)
	if err != nil {
		return err
	}

	runDir, err := os.MkdirTemp("", "c2vm")
	if err != nil {
		return err
	}
	defer os.RemoveAll(runDir)

	initrd := filepath.Join(runDir, "initrd.cpio")
	if err := writeInitramfs(initrd, initBinary, config); err != nil {
		return fmt.Errorf("failed to build the initramfs: %w", err)
	}

	socketPath := opts.socketPath
	if socketPath == "" {
		socketPath = filepath.Join(runDir, "firecracker.sock")
	}

	return vm.Run(ctx, vm.Config{
		FirecrackerBinary: opts.firecrackerBinary,
		SocketPath:        socketPath,
		Kernel:            opts.kernel,
		Initrd:            initrd,
		Image:             opts.outFile,
		MetricsFifo:       opts.metricsFifo,
		CPUs:              opts.cpus,
		MemoryMiB:         opts.memoryMiB,
		CNINetwork:        opts.cniNetwork,
	})
}

// buildRootfs creates the image at rawFile, mounts it, and populates it
// with the container's filesystem. The image is always unmounted before
// returning.
func buildRootfs(ctx context.Context, img *image.Image, rawFile, size string) (err error) {
	if err := rootfs.Create(rawFile, size); err != nil {
		return err
	}

	mntDir, unmount, err := rootfs.Mount(rawFile)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unmount(); uerr != nil && err == nil {
			err = uerr
		}
	}()

	if err := img.Unpack(ctx, mntDir); err != nil {
		return fmt.Errorf("failed to extract the container: %w", err)
	}

	return nil
}

// findInit returns the path to the c2vm-init binary: path if set,
// otherwise c2vm-init in the same directory as c2vm.
func findInit(path string) (string, error) {
	if path == "" {
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		path = filepath.Join(filepath.Dir(self), "c2vm-init")
	}

	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("c2vm-init not found (build it with make, or set -init): %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	return path, nil
}

func writeInitramfs(path, initBinary string, config guest.Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := initramfs.Write(f, initBinary, config); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
