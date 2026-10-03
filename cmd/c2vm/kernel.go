package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"fcuny.net/containerd-to-vm/internal/cache"
	"fcuny.net/containerd-to-vm/internal/image"
)

// defaultKernel is the image of the kernel VMs boot by default, built by
// .github/workflows/kernel.yml.
const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:6.18"

// kernelPath is where kernel images keep the kernel.
const kernelPath = "/boot/kernel"

// resolveKernel returns the path of the kernel to boot. kernel is either
// a file, used as is, or an image whose /boot/kernel is extracted, for
// the host's architecture, into the cache.
func resolveKernel(ctx context.Context, kernel, cacheDir string) (string, error) {
	if fi, err := os.Stat(kernel); err == nil {
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("kernel %s is not a regular file", kernel)
		}
		return kernel, nil
	}

	// The hypervisor can only run guests of the host's architecture,
	// whatever the image's platform is.
	img, err := image.Pull(ctx, kernel, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
	if err != nil {
		return "", fmt.Errorf("kernel %q is neither a file nor a pullable image: %w", kernel, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return "", err
	}

	path, cached, err := cache.New(cacheDir).Get("kernels", digest, func(w io.ReadWriteSeeker) error {
		return img.ExtractFile(kernelPath, w)
	})
	if err != nil {
		return "", err
	}
	if !cached {
		log.Printf("extracted the kernel to %s\n", path)
	}
	return path, nil
}
