package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"fcuny.net/c2vm/internal/cache"
)

// defaultKernel is the image of the kernel VMs boot by default, built by
// .github/workflows/kernel.yml.
const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:7.3"

// Where kernel images, and directories laid out like them, keep their
// files.
const (
	// kernelPath is the kernel.
	kernelPath = "/boot/kernel"
	// mkfsPath is a static mkfs.ext4, which init formats a scratch disk
	// with.
	mkfsPath = "/sbin/mkfs.ext4"
)

// kernelFiles are the files a VM boots with, from its kernel image.
type kernelFiles struct {
	// kernel is the path of the kernel.
	kernel string
	// mkfs is the path of mkfs.ext4, when it was asked for.
	mkfs string
}

// resolveKernel returns the files to boot with, extracting them from
// kernel when it's an image. kernel is one of:
//
//   - a file: the kernel, used as is. It has no mkfs.ext4.
//   - a directory laid out like a kernel image, e.g. one extracted from
//     the kernel workflow's artifact: its files are used as they are.
//     Its mkfs.ext4 has to be static, as readMkfs checks.
//   - an image: the files are extracted, for the host's architecture,
//     into the cache.
//
// mkfs.ext4 is only looked for when withMkfs is set: a VM without a
// scratch disk doesn't need it, and older kernel images don't have it.
func resolveKernel(ctx context.Context, kernel, cacheDir string, withMkfs bool) (kernelFiles, error) {
	if fi, err := os.Stat(kernel); err == nil {
		if fi.IsDir() {
			return kernelDir(kernel, withMkfs)
		}
		if !fi.Mode().IsRegular() {
			return kernelFiles{}, fmt.Errorf("kernel %s is neither a regular file nor a directory", kernel)
		}
		if withMkfs {
			return kernelFiles{}, fmt.Errorf("kernel %s is a file, without the mkfs.ext4 a scratch disk needs: use a kernel image, or a directory with %s and %s", kernel, kernelPath[1:], mkfsPath[1:])
		}
		return kernelFiles{kernel: kernel}, nil
	}

	// The hypervisor can only run guests of the host's architecture,
	// whatever the image's platform is.
	c := cache.New(cacheDir)
	kinds := []string{"kernels"}
	if withMkfs {
		kinds = append(kinds, "mkfs")
	}
	platform := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	res, err := resolve(ctx, c, kernel, platform, kinds...)
	if err != nil {
		// The registry couldn't answer, and only mkfs.ext4 is missing from
		// the cache: say so, rather than only why the registry failed.
		var partly *partlyCachedError
		if errors.As(err, &partly) && slices.Equal(partly.missing, []string{"mkfs"}) {
			return kernelFiles{}, fmt.Errorf("a scratch disk needs mkfs.ext4 from kernel %q, which isn't in the cache, and the registry can't be reached to get it: %w", kernel, err)
		}
		return kernelFiles{}, fmt.Errorf("kernel %q is neither a file, a directory nor a pullable image: %w", kernel, err)
	}

	// The image's files, read in one pass the first time one isn't
	// cached: a pulled image's layers come from the registry each time
	// they're read.
	var contents map[string][]byte
	read := func(path string) func(io.ReadWriteSeeker) error {
		return func(w io.ReadWriteSeeker) error {
			if res.img == nil {
				return errors.New("it was removed from the cache while c2vm used it")
			}
			if contents == nil {
				var err error
				if contents, err = res.img.ReadFiles(kernelPath, mkfsPath); err != nil {
					return err
				}
			}
			data, ok := contents[path]
			if !ok {
				return fmt.Errorf("%s has no %s", kernel, path)
			}
			_, err := w.Write(data)
			return err
		}
	}

	var files kernelFiles
	if files.kernel, err = extract(c, res.digest, "kernels", read(kernelPath), "the kernel"); err != nil {
		return kernelFiles{}, err
	}
	if withMkfs {
		if files.mkfs, err = extract(c, res.digest, "mkfs", read(mkfsPath), "mkfs.ext4"); err != nil {
			return kernelFiles{}, fmt.Errorf("a scratch disk needs mkfs.ext4 from the kernel image: %w", err)
		}
	} else if _, ok := contents[mkfsPath]; ok {
		// The image was read anyway: cache mkfs.ext4 too, so that a later
		// boot with -scratch does without the registry. Kernel images
		// from before it was added don't have it.
		extract(c, res.digest, "mkfs", read(mkfsPath), "mkfs.ext4")
	}
	if err := res.record(); err != nil {
		return kernelFiles{}, err
	}
	return files, nil
}

// readMkfs reads the mkfs.ext4 at path, and checks that init can run it
// from the initramfs: a static arm64 Linux binary. A dynamic one would
// fail in the guest with a misleading "no such file or directory", for
// its missing interpreter.
func readMkfs(path string) ([]byte, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if err := checkStatic(data); err != nil {
		return nil, fmt.Errorf("mkfs.ext4 %s %w", path, err)
	}
	return data, nil
}

// extract returns the path of the file of kind for digest in the cache,
// which create writes there if it isn't yet. what names it in the log.
func extract(c *cache.Cache, digest v1.Hash, kind string, create func(io.ReadWriteSeeker) error, what string) (string, error) {
	extracted, cached, err := c.Get(kind, digest, create)
	if err != nil {
		return "", err
	}
	if !cached {
		log.Printf("extracted %s to %s\n", what, extracted)
	}
	return extracted, nil
}

// kernelDir returns the files of dir, laid out like a kernel image. The
// kernel has to be a regular file, which nothing checks before the
// hypervisor loads it; readMkfs checks mkfs.ext4 as it reads it.
func kernelDir(dir string, withMkfs bool) (kernelFiles, error) {
	find := func(path string) (string, os.FileInfo, error) {
		full := filepath.Join(dir, filepath.FromSlash(path))
		fi, err := os.Stat(full)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("kernel directory %s has no %s", dir, path[1:])
		}
		return full, fi, err
	}

	var files kernelFiles
	full, fi, err := find(kernelPath)
	if err != nil {
		return kernelFiles{}, err
	}
	if !fi.Mode().IsRegular() {
		return kernelFiles{}, fmt.Errorf("%s is not a regular file", full)
	}
	files.kernel = full
	if withMkfs {
		if files.mkfs, _, err = find(mkfsPath); err != nil {
			return kernelFiles{}, fmt.Errorf("a scratch disk needs mkfs.ext4: %w", err)
		}
	}
	return files, nil
}
