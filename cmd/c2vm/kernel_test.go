package main

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	got, err := resolveKernel(context.Background(), kernel, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != kernel {
		t.Errorf("resolveKernel = %s, want the file itself", got)
	}

	if _, err := resolveKernel(context.Background(), t.TempDir(), t.TempDir()); err == nil {
		t.Error("a directory shouldn't be accepted as a kernel")
	}
}

func TestResolveKernelImage(t *testing.T) {
	ref := pushKernelImage(t, "the kernel")
	cacheDir := t.TempDir()

	path, err := resolveKernel(context.Background(), ref, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, cacheDir) {
		t.Errorf("kernel at %s, want it in the cache", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "the kernel" {
		t.Errorf("kernel contains %q", data)
	}
}

func TestResolveKernelInvalid(t *testing.T) {
	_, err := resolveKernel(context.Background(), "not a file:or an image", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "neither a file nor a pullable image") {
		t.Errorf("resolveKernel = %v", err)
	}
}

// pushKernelImage pushes a kernel image for the host's architecture to
// an in-memory registry, and returns its reference.
func pushKernelImage(t *testing.T, kernel string) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "boot/", Mode: 0o755, Typeflag: tar.TypeDir})
	tw.WriteHeader(&tar.Header{Name: "boot/kernel", Mode: 0o644, Size: int64(len(kernel)), Typeflag: tar.TypeReg})
	io.WriteString(tw, kernel)
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
	return ref
}
