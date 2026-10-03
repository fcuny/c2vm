// Command c2vm boots a container image as a firecracker microVM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/platforms"
	"github.com/google/renameio/v2"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"fcuny.net/containerd-to-vm/internal/image"
	"fcuny.net/containerd-to-vm/internal/initscript"
	"fcuny.net/containerd-to-vm/internal/rootfs"
	"fcuny.net/containerd-to-vm/internal/vm"
)

const (
	containerdSock   = "/run/containerd/containerd.sock"
	defaultNamespace = "c2vm"
	initPath         = "/init.sh"
)

var (
	firecrackerSock = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "firecracker.sock")
	platform        = platforms.Only(ocispec.Platform{
		OS:           "linux",
		Architecture: "amd64",
	})
)

func main() {
	var (
		containerName     = flag.String("container", "", "Name of the container")
		outFile           = flag.String("out", "container.img", "Path to store the image")
		kernel            = flag.String("kernel", "", "Path to the linux kernel image")
		firecrackerBinary = flag.String("firecracker-binary", "", "Path to the firecracker binary")
		metricsFifo       = flag.String("metrics-fifo", "", "FIFO to the firecracker metrics")
	)

	flag.Parse()

	if *containerName == "" {
		log.Fatal("a container is required")
	}

	if *kernel == "" {
		log.Fatal("a linux kernel is required")
	}

	if *firecrackerBinary == "" {
		log.Fatal("the path to the firecracker binary is required")
	}

	if err := run(*containerName, *outFile, *kernel, *firecrackerBinary, *metricsFifo); err != nil {
		log.Fatal(err)
	}
}

func run(containerName, outFile, kernel, firecrackerBinary, metricsFifo string) error {
	c, err := client.New(containerdSock)
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

	img, err := image.Pull(ctx, c, containerName, platform)
	if err != nil {
		return err
	}

	if err := buildRootfs(ctx, img, outFile); err != nil {
		return err
	}

	if err := rootfs.Shrink(outFile); err != nil {
		return fmt.Errorf("failed to resize the image %s: %w", outFile, err)
	}

	return vm.Run(ctx, vm.Config{
		FirecrackerBinary: firecrackerBinary,
		SocketPath:        firecrackerSock,
		Kernel:            kernel,
		Init:              initPath,
		RootDrive:         outFile,
		MetricsFifo:       metricsFifo,
		CPUs:              1,
		MemoryMiB:         512,
		CNINetwork:        "c2vm",
	})
}

// buildRootfs creates the image at rawFile, mounts it, and populates it
// with the container's filesystem. The image is always unmounted before
// returning.
func buildRootfs(ctx context.Context, img *image.Image, rawFile string) (err error) {
	if err := rootfs.Create(rawFile, "2G"); err != nil {
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

	if err := writeInitScript(ctx, img, mntDir); err != nil {
		return fmt.Errorf("failed to create init script: %w", err)
	}

	if err := rootfs.WriteExtraFiles(mntDir); err != nil {
		return fmt.Errorf("failed to add extra files to the image: %w", err)
	}

	return nil
}

func writeInitScript(ctx context.Context, img *image.Image, mntDir string) error {
	config, err := img.Config(ctx)
	if err != nil {
		return err
	}

	script, err := initscript.Generate(config)
	if err != nil {
		return err
	}

	f, err := renameio.NewPendingFile(filepath.Join(mntDir, initPath))
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if _, err := f.WriteString(script); err != nil {
		return err
	}

	if err := f.Chmod(0755); err != nil {
		return err
	}

	if err := f.CloseAtomicallyReplace(); err != nil {
		return err
	}

	log.Printf("init script created")
	return nil
}
