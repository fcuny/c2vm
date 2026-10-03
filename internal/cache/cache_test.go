package cache

import (
	"errors"
	"io"
	"os"
	"testing"

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
