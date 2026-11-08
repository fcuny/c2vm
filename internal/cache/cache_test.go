package cache

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

var digest = v1.Hash{Algorithm: "sha256", Hex: "4f3b"}

func TestGet(t *testing.T) {
	c := New(t.TempDir())

	calls := 0
	create := func(w io.ReadWriteSeeker) error {
		calls++
		_, err := io.WriteString(w, "image")
		return err
	}

	path, cached, err := c.Get("images", digest, create)
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("first Get reported a cached file")
	}
	if path != c.Path("images", digest) {
		t.Errorf("path = %s, want %s", path, c.Path("images", digest))
	}
	if data, _ := os.ReadFile(path); string(data) != "image" {
		t.Errorf("file contains %q", data)
	}

	path2, cached, err := c.Get("images", digest, create)
	if err != nil {
		t.Fatal(err)
	}
	if !cached || path2 != path || calls != 1 {
		t.Errorf("second Get: cached=%v path=%s calls=%d, want it served from the cache", cached, path2, calls)
	}
}

func TestGetFailure(t *testing.T) {
	c := New(t.TempDir())

	_, _, err := c.Get("images", digest, func(w io.ReadWriteSeeker) error {
		io.WriteString(w, "half an image")
		return errors.New("network went away")
	})
	if err == nil {
		t.Fatal("expected the error from create")
	}
	if _, err := os.Stat(c.Path("images", digest)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed create left a file in the cache: %v", err)
	}
}

func TestHas(t *testing.T) {
	c := New(t.TempDir())
	if c.Has("images", digest) {
		t.Error("Has before Get")
	}
	if _, _, err := c.Get("images", digest, func(w io.ReadWriteSeeker) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !c.Has("images", digest) || c.Has("kernels", digest) {
		t.Error("Has should only find the kind that was written")
	}
}

func sha256Hash(t *testing.T, s string) v1.Hash {
	t.Helper()
	h, _, err := v1.SHA256(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRef(t *testing.T) {
	c := New(t.TempDir())
	arm64 := v1.Platform{OS: "linux", Architecture: "arm64"}
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	const name = "index.docker.io/library/alpine:3.24"
	// Records keep the digest as text, which must be a full one.
	digest, newer := sha256Hash(t, "image"), sha256Hash(t, "newer image")

	if _, ok, err := c.Ref(name, arm64); ok || err != nil {
		t.Fatalf("Ref before SetRef: ok=%v, err=%v", ok, err)
	}

	before := time.Now()
	if err := c.SetRef(name, arm64, digest); err != nil {
		t.Fatal(err)
	}
	r, ok, err := c.Ref(name, arm64)
	if !ok || err != nil {
		t.Fatalf("Ref after SetRef: ok=%v, err=%v", ok, err)
	}
	if r.Name != name || r.Platform != "linux/arm64" || r.Digest != digest || r.Resolved.Before(before) {
		t.Errorf("Ref = %+v", r)
	}

	// Each platform has its own record.
	if _, ok, _ := c.Ref(name, amd64); ok {
		t.Error("the arm64 record answered for amd64")
	}

	// A new lookup replaces the record.
	if err := c.SetRef(name, arm64, newer); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := c.Ref(name, arm64); r.Digest != newer {
		t.Errorf("Ref after a second SetRef = %v, want %v", r.Digest, newer)
	}
}

func TestRefCorrupt(t *testing.T) {
	c := New(t.TempDir())
	p := v1.Platform{OS: "linux", Architecture: "arm64"}
	if err := os.MkdirAll(filepath.Dir(c.refPath("alpine", p)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.refPath("alpine", p), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Ref("alpine", p); err == nil || !strings.Contains(err.Error(), c.refPath("alpine", p)) {
		t.Errorf("corrupt record: error = %v, want one naming the file", err)
	}
}
