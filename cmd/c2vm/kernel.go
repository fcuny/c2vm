package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"fcuny.net/c2vm/internal/cache"
	"fcuny.net/c2vm/internal/image"
)

// defaultKernel is the image of the kernel VMs boot by default, built by
// .github/workflows/kernel.yml.
const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:7.3"

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
	c := cache.New(cacheDir)
	res, err := resolve(ctx, c, kernel, v1.Platform{OS: "linux", Architecture: runtime.GOARCH}, "kernels")
	if err != nil {
		return "", fmt.Errorf("kernel %q is neither a file nor a pullable image: %w", kernel, err)
	}

	path, cached, err := c.Get("kernels", res.digest, fromImage(res.img, func(img *image.Image, w io.ReadWriteSeeker) error {
		return img.ExtractFile(kernelPath, w)
	}))
	if err != nil {
		return "", err
	}
	if err := res.record(); err != nil {
		return "", err
	}
	if !cached {
		log.Printf("extracted the kernel to %s\n", path)
	}
	return path, nil
}
