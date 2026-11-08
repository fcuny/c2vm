// Package cache stores files c2vm derives from images, keyed by the
// image's digest, so they're only built once, and what each image
// reference last resolved to.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

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

// Has reports whether the file of the given kind for digest is cached.
func (c *Cache) Has(kind string, digest v1.Hash) bool {
	_, err := os.Stat(c.Path(kind, digest))
	return err == nil
}

// Ref is what an image reference resolved to, for a platform, the last
// time the registry was asked.
type Ref struct {
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	Digest   v1.Hash   `json:"digest"`
	Resolved time.Time `json:"resolved"`
}

// refPath is where the Ref for name and platform is stored.
func (c *Cache) refPath(name string, platform v1.Platform) string {
	key := sha256.Sum256([]byte(name + "\x00" + platform.String()))
	return filepath.Join(c.dir, "refs", hex.EncodeToString(key[:])+".json")
}

// Ref returns what name last resolved to for platform, and false if
// that wasn't recorded.
func (c *Cache) Ref(name string, platform v1.Platform) (Ref, bool, error) {
	path := c.refPath(name, platform)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Ref{}, false, nil
	} else if err != nil {
		return Ref{}, false, err
	}
	var r Ref
	if err := json.Unmarshal(data, &r); err != nil {
		return Ref{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return r, true, nil
}

// SetRef records that name resolved to digest for platform, now.
func (c *Cache) SetRef(name string, platform v1.Platform, digest v1.Hash) error {
	data, err := json.Marshal(Ref{Name: name, Platform: platform.String(), Digest: digest, Resolved: time.Now()})
	if err != nil {
		return err
	}
	path := c.refPath(name, platform)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return renameio.WriteFile(path, data, 0o644)
}
