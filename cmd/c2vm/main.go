// Command c2vm boots a container image as a microVM.
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

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/renameio/v2"

	"fcuny.net/containerd-to-vm/internal/guest"
	"fcuny.net/containerd-to-vm/internal/image"
	"fcuny.net/containerd-to-vm/internal/initramfs"
	"fcuny.net/containerd-to-vm/internal/vm"
)

type options struct {
	container  string
	outFile    string
	kernel     string
	initBinary string
	cpus       int64
	memoryMiB  int64
	platform   v1.Platform
	backend    func() (vm.Backend, error)
}

func parseFlags(args []string) (options, error) {
	var (
		opts     options
		platform string
	)

	fs := flag.NewFlagSet("c2vm", flag.ContinueOnError)
	fs.StringVar(&opts.container, "container", "", "Image to boot (e.g. alpine:3.24, or ghcr.io/owner/image:tag)")
	fs.StringVar(&opts.outFile, "out", "container.img", "Path to store the image")
	fs.StringVar(&opts.kernel, "kernel", "", "Path to the linux kernel image")
	fs.StringVar(&opts.initBinary, "init", "", "Path to the c2vm-init binary (default: c2vm-init next to c2vm)")
	fs.Int64Var(&opts.cpus, "cpus", 1, "Number of vCPUs")
	fs.Int64Var(&opts.memoryMiB, "memory", 512, "Memory for the VM, in MiB")
	fs.StringVar(&platform, "platform", "linux/"+runtime.GOARCH, "Platform of the image to pull")
	opts.backend = backendFlags(fs)

	if err := fs.Parse(args); err != nil {
		return options{}, err
	}

	if opts.container == "" {
		return options{}, errors.New("a container is required")
	}
	if opts.kernel == "" {
		return options{}, errors.New("a linux kernel is required")
	}
	if opts.cpus < 1 {
		return options{}, errors.New("-cpus must be at least 1")
	}
	if opts.memoryMiB < 1 {
		return options{}, errors.New("-memory must be at least 1")
	}

	p, err := v1.ParsePlatform(platform)
	if err != nil {
		return options{}, fmt.Errorf("invalid -platform: %w", err)
	}
	if p.OS != "linux" {
		return options{}, fmt.Errorf("invalid -platform %q: only linux images can be booted", platform)
	}
	opts.platform = *p

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
	backend, err := opts.backend()
	if err != nil {
		return err
	}

	initBinary, err := findInit(opts.initBinary)
	if err != nil {
		return err
	}

	ctx := context.Background()

	img, err := image.Pull(ctx, opts.container, opts.platform)
	if err != nil {
		return err
	}

	if err := writeImage(img, opts.outFile); err != nil {
		return err
	}
	log.Printf("wrote %s\n", opts.outFile)

	imageConfig, err := img.Config()
	if err != nil {
		return err
	}
	config, err := guest.FromImage(imageConfig)
	if err != nil {
		return err
	}
	config.Shutdown = backend.Shutdown()

	runDir, err := os.MkdirTemp("", "c2vm")
	if err != nil {
		return err
	}
	defer os.RemoveAll(runDir)

	initrd := filepath.Join(runDir, "initrd.cpio")
	if err := writeInitramfs(initrd, initBinary, config); err != nil {
		return fmt.Errorf("failed to build the initramfs: %w", err)
	}

	return backend.Run(ctx, vm.Spec{
		Kernel:    opts.kernel,
		Initrd:    initrd,
		Image:     opts.outFile,
		CPUs:      opts.cpus,
		MemoryMiB: opts.memoryMiB,
	})
}

// writeImage writes the image's filesystem to path, atomically.
func writeImage(img *image.Image, path string) error {
	f, err := renameio.NewPendingFile(path, renameio.WithPermissions(0644))
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if err := img.WriteExt4(f); err != nil {
		return err
	}
	return f.CloseAtomicallyReplace()
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
