package image

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

type file struct {
	name string
	data string
	dir  bool
}

func layer(t *testing.T, files ...file) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: tar.TypeReg}
		if f.dir {
			hdr = &tar.Header{Name: f.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// pushTestImage pushes an image with the given layers to an in-memory
// registry, and returns its reference.
func pushTestImage(t *testing.T, layers ...v1.Layer) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.ConfigFile(img, &v1.ConfigFile{
		OS:           "linux",
		Architecture: "arm64",
		Config:       v1.Config{Cmd: []string{"/hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ref := u.Host + "/test/image:latest"
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	return ref
}

func pull(t *testing.T, ref string) *Image {
	t.Helper()
	img, err := Pull(context.Background(), ref, v1.Platform{OS: "linux", Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestExtractFile(t *testing.T) {
	ref := pushTestImage(t,
		layer(t, file{name: "boot", dir: true}, file{name: "boot/kernel", data: "old kernel"}),
		// A later layer replaces the file.
		layer(t, file{name: "./boot/kernel", data: "new kernel"}, file{name: "boot/config", data: "CONFIG_X=y"}),
	)
	img := pull(t, ref)

	var buf bytes.Buffer
	if err := img.ExtractFile("/boot/kernel", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "new kernel" {
		t.Errorf("extracted %q, want the last layer's version", buf.String())
	}

	err := img.ExtractFile("/boot/missing", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "has no /boot/missing") {
		t.Errorf("ExtractFile of a missing file = %v", err)
	}
	if err := img.ExtractFile("/boot", io.Discard); err == nil {
		t.Error("ExtractFile of a directory should fail")
	}
}

func TestConfigAndDigest(t *testing.T) {
	img := pull(t, pushTestImage(t, layer(t, file{name: "hello", data: "#!"})))

	config, err := img.Config()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Cmd) != 1 || config.Cmd[0] != "/hello" {
		t.Errorf("Cmd = %q", config.Cmd)
	}

	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest.Algorithm != "sha256" || len(digest.Hex) != 64 {
		t.Errorf("unexpected digest %v", digest)
	}
}

func TestWriteExt4(t *testing.T) {
	img := pull(t, pushTestImage(t, layer(t, file{name: "etc", dir: true}, file{name: "etc/hostname", data: "vm\n"})))

	f, err := os.Create(filepath.Join(t.TempDir(), "image.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := img.WriteExt4(f); err != nil {
		t.Fatal(err)
	}

	// The ext4 superblock starts at 1024 bytes, with its magic number,
	// 0xEF53, 56 bytes in.
	var magic uint16
	if _, err := f.Seek(1024+56, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		t.Fatal(err)
	}
	if magic != 0xEF53 {
		t.Errorf("superblock magic = %#x, want 0xef53", magic)
	}
}
