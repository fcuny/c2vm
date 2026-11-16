package main

import (
	"archive/tar"
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func TestResolveKernelFile(t *testing.T) {
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveKernel(context.Background(), kernel, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got != (kernelFiles{kernel: kernel}) {
		t.Errorf("resolveKernel = %+v, want the file itself", got)
	}

	// A file is only the kernel: a scratch disk needs mkfs.ext4 too.
	_, err = resolveKernel(context.Background(), kernel, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), "mkfs.ext4") {
		t.Errorf("resolveKernel with mkfs = %v, want an error about mkfs.ext4", err)
	}
}

func TestResolveKernelDir(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"boot/kernel": "kernel", "sbin/mkfs.ext4": "mkfs"})

	got, err := resolveKernel(context.Background(), dir, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	want := kernelFiles{kernel: filepath.Join(dir, "boot", "kernel"), mkfs: filepath.Join(dir, "sbin", "mkfs.ext4")}
	if got != want {
		t.Errorf("resolveKernel = %+v, want %+v", got, want)
	}

	got, err = resolveKernel(context.Background(), dir, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got != (kernelFiles{kernel: want.kernel}) {
		t.Errorf("resolveKernel without mkfs = %+v, want only the kernel", got)
	}
}

func TestResolveKernelDirMissing(t *testing.T) {
	for _, tt := range []struct {
		name     string
		files    map[string]string
		withMkfs bool
		want     string
	}{
		{"empty", nil, false, "has no boot/kernel"},
		{"no mkfs", map[string]string{"boot/kernel": "kernel"}, true, "has no sbin/mkfs.ext4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			_, err := resolveKernel(context.Background(), dir, t.TempDir(), tt.withMkfs)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("resolveKernel = %v, want an error with %q", err, tt.want)
			}
		})
	}

	// A kernel without mkfs.ext4 is fine without a scratch disk.
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"boot/kernel": "kernel"})
	if _, err := resolveKernel(context.Background(), dir, t.TempDir(), false); err != nil {
		t.Errorf("resolveKernel without mkfs: %v", err)
	}
}

func TestResolveKernelImage(t *testing.T) {
	ref, _ := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel"})
	cacheDir := t.TempDir()

	files, err := resolveKernel(context.Background(), ref, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(files.kernel, cacheDir) {
		t.Errorf("kernel at %s, want it in the cache", files.kernel)
	}
	if data, _ := os.ReadFile(files.kernel); string(data) != "the kernel" {
		t.Errorf("kernel contains %q", data)
	}
	if files.mkfs != "" {
		t.Errorf("mkfs at %s, but it wasn't asked for", files.mkfs)
	}
}

func TestResolveKernelImageMkfs(t *testing.T) {
	ref, srv := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel", "sbin/mkfs.ext4": "mkfs"})
	cacheDir := t.TempDir()

	files, err := resolveKernel(context.Background(), ref, cacheDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(files.mkfs, cacheDir) {
		t.Errorf("mkfs at %s, want it in the cache", files.mkfs)
	}
	if data, _ := os.ReadFile(files.mkfs); string(data) != "mkfs" {
		t.Errorf("mkfs contains %q", data)
	}

	// Both are cached now: a boot does without the registry.
	captureLog(t)
	srv.Close()
	offline, err := resolveKernel(context.Background(), ref, cacheDir, true)
	if err != nil {
		t.Fatalf("resolveKernel with the registry down: %v", err)
	}
	if offline != files {
		t.Errorf("offline = %+v, want %+v", offline, files)
	}
}

// TestResolveKernelOnePass checks that resolving a kernel reads the
// image's layer from the registry once, whether or not mkfs.ext4 is
// asked for: both files come from the same pass over it.
func TestResolveKernelOnePass(t *testing.T) {
	for _, withMkfs := range []bool{false, true} {
		t.Run(fmt.Sprintf("withMkfs=%v", withMkfs), func(t *testing.T) {
			ref, _, served := pushKernelImageCounting(t, map[string]string{"boot/kernel": "the kernel", "sbin/mkfs.ext4": "mkfs"})
			if _, err := resolveKernel(context.Background(), ref, t.TempDir(), withMkfs); err != nil {
				t.Fatal(err)
			}
			if n := served(); n != 1 {
				t.Errorf("the layer was served %d times, want once", n)
			}
		})
	}
}

// TestResolveKernelMkfsCachedEarly checks that a boot without a scratch
// disk caches mkfs.ext4 too, when it pulls the kernel image anyway, so
// that a later boot with one works without the registry.
func TestResolveKernelMkfsCachedEarly(t *testing.T) {
	ref, srv := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel", "sbin/mkfs.ext4": "mkfs"})
	cacheDir := t.TempDir()
	captureLog(t)

	if _, err := resolveKernel(context.Background(), ref, cacheDir, false); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	files, err := resolveKernel(context.Background(), ref, cacheDir, true)
	if err != nil {
		t.Fatalf("resolveKernel with -scratch, the registry down: %v", err)
	}
	if data, _ := os.ReadFile(files.mkfs); string(data) != "mkfs" {
		t.Errorf("mkfs contains %q", data)
	}
}

// TestResolveKernelMkfsNotCached checks that a boot with a scratch disk
// says mkfs.ext4 is what's missing when the kernel is cached without it
// and the registry is down, rather than only why the registry failed.
func TestResolveKernelMkfsNotCached(t *testing.T) {
	// A kernel image from before mkfs.ext4 was added.
	ref, srv := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel"})
	cacheDir := t.TempDir()
	captureLog(t)

	if _, err := resolveKernel(context.Background(), ref, cacheDir, false); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err := resolveKernel(context.Background(), ref, cacheDir, true)
	if err == nil || !strings.Contains(err.Error(), "mkfs.ext4") || !strings.Contains(err.Error(), "isn't in the cache") {
		t.Errorf("resolveKernel = %v, want an error saying mkfs.ext4 isn't cached", err)
	}
}

func TestReadMkfs(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{"static arm64", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, false), ""},
		{"dynamic", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_AARCH64, true), "is dynamically linked, it must be static"},
		{"x86-64", fakeELF(elf.ELFCLASS64, elf.ET_EXEC, elf.EM_X86_64, false), "not EM_AARCH64"},
		{"not an ELF", []byte("#!/bin/sh\n"), "is not an ELF binary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mkfs.ext4")
			if err := os.WriteFile(path, tt.data, 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := readMkfs(path)
			if tt.want == "" {
				if err != nil || !bytes.Equal(data, tt.data) {
					t.Errorf("readMkfs = %d bytes, %v, want the file", len(data), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("readMkfs = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestResolveKernelImageNoMkfs checks that a kernel image from before
// mkfs.ext4 was added still boots without a scratch disk, and that one
// with a scratch disk says what's missing.
func TestResolveKernelImageNoMkfs(t *testing.T) {
	ref, _ := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel"})

	_, err := resolveKernel(context.Background(), ref, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), "has no /sbin/mkfs.ext4") {
		t.Errorf("resolveKernel = %v, want an error about mkfs.ext4", err)
	}
}

// TestResolveKernelOffline checks a kernel booted by tag comes from the
// cache, with a warning, when the registry is down.
func TestResolveKernelOffline(t *testing.T) {
	ref, srv := pushKernelImage(t, map[string]string{"boot/kernel": "the kernel"})
	logs := captureLog(t)
	cacheDir := t.TempDir()

	files, err := resolveKernel(context.Background(), ref, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}

	srv.Close()
	offline, err := resolveKernel(context.Background(), ref, cacheDir, false)
	if err != nil {
		t.Fatalf("resolveKernel with the registry down: %v", err)
	}
	if offline != files {
		t.Errorf("kernel at %+v, want %+v", offline, files)
	}
	if data, _ := os.ReadFile(offline.kernel); string(data) != "the kernel" {
		t.Errorf("kernel contains %q", data)
	}
	if !strings.Contains(logs.String(), "may be out of date") {
		t.Errorf("no warning in the log:\n%s", logs)
	}
}

func TestResolveKernelInvalid(t *testing.T) {
	_, err := resolveKernel(context.Background(), "not a file:or an image", t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "neither a file, a directory nor a pullable image") {
		t.Errorf("resolveKernel = %v", err)
	}
}

// writeFiles writes files, by slash-separated path, under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// pushKernelImage pushes a kernel image for the host's architecture,
// with files by path, to an in-memory registry, and returns its
// reference and the registry.
func pushKernelImage(t *testing.T, files map[string]string) (string, *httptest.Server) {
	t.Helper()
	ref, srv, _ := pushKernelImageCounting(t, files)
	return ref, srv
}

// pushKernelImageCounting is pushKernelImage, and also returns how many
// times the registry has served the image's layer so far.
func pushKernelImageCounting(t *testing.T, files map[string]string) (string, *httptest.Server, func() int64) {
	t.Helper()
	var mu sync.Mutex
	served := map[string]int64{}
	reg := registry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, digest, ok := strings.Cut(r.URL.Path, "/blobs/"); ok && r.Method == http.MethodGet {
			mu.Lock()
			served[digest]++
			mu.Unlock()
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	count := func(digest string) int64 {
		mu.Lock()
		defer mu.Unlock()
		return served[digest]
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for name, data := range files {
		if dir := filepath.Dir(name); !dirs[dir] {
			dirs[dir] = true
			tw.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		io.WriteString(tw, data)
	}
	tw.Close()
	data := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.ConfigFile(img, &v1.ConfigFile{OS: "linux", Architecture: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}

	ref := u.Host + "/c2vm-kernel:test"
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	digest, err := layer.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return ref, srv, func() int64 { return count(digest.String()) }
}
