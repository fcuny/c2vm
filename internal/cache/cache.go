// Package cache stores files c2vm derives from images, keyed by the
// image's digest, so they're only built once.
package cache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/renameio/v2"
)

// Cache is a directory of files keyed by digest.
type Cache struct {
	dir string
}

// New returns a cache in dir.
func New(dir string) *Cache {
	return &Cache{dir: dir}
}

// DefaultDir is the user's cache directory for c2vm: ~/.cache/c2vm on
// Linux, ~/Library/Caches/c2vm on macOS.
func DefaultDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "c2vm"), nil
}

// Path is where the file of the given kind for digest is stored.
func (c *Cache) Path(kind string, digest v1.Hash) string {
	return filepath.Join(c.dir, kind, digest.Algorithm+"-"+digest.Hex)
}

// Get returns the path of the file of the given kind for digest,
// calling create to write it first if it isn't cached. The file only
// appears in the cache once create succeeds.
func (c *Cache) Get(kind string, digest v1.Hash, create func(w io.ReadWriteSeeker) error) (path string, cached bool, err error) {
	path = c.Path(kind, digest)
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}

	f, err := renameio.NewPendingFile(path, renameio.WithPermissions(0o644))
	if err != nil {
		return "", false, err
	}
	defer f.Cleanup()

	if err := create(f); err != nil {
		return "", false, fmt.Errorf("creating %s: %w", path, err)
	}
	if err := f.CloseAtomicallyReplace(); err != nil {
		return "", false, err
	}
	return path, false, nil
}
