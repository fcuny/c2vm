package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"fcuny.net/c2vm/internal/cache"
)

var arm64 = v1.Platform{OS: "linux", Architecture: "arm64"}

// flakyRegistry is an in-memory registry with one image, which answers
// requests for manifests with an error while fail is set.
type flakyRegistry struct {
	srv *httptest.Server
	// tag and repo name the image, by tag and without one.
	tag, repo string
	digest    v1.Hash
	// fail is the status and error code to answer with, as "404
	// MANIFEST_UNKNOWN", or empty to serve the image.
	fail atomic.Value
	// failBlobs makes requests for blobs (layers and configs) fail, after
	// the manifest was served.
	failBlobs atomic.Bool
	requests  atomic.Int32
}

func newFlakyRegistry(t *testing.T) *flakyRegistry {
	t.Helper()
	reg := &flakyRegistry{}
	reg.fail.Store("")
	backend := registry.New()
	reg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reg.requests.Add(1)
		var code int
		var errCode string
		if fail := reg.fail.Load().(string); fail != "" && strings.Contains(r.URL.Path, "/manifests/") {
			fmt.Sscanf(fail, "%d %s", &code, &errCode)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"injected by the test"}]}`, errCode)
			return
		}
		// 501 rather than 500, which go-containerregistry retries for
		// seconds.
		if reg.failBlobs.Load() && strings.Contains(r.URL.Path, "/blobs/") {
			http.Error(w, "injected by the test", http.StatusNotImplemented)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(reg.srv.Close)

	u, err := url.Parse(reg.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	reg.repo = u.Host + "/test/image"
	reg.tag = reg.repo + ":latest"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "etc/", Mode: 0o755, Typeflag: tar.TypeDir})
	tw.WriteHeader(&tar.Header{Name: "etc/hostname", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	io.WriteString(tw, "vm\n")
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
	img, err = mutate.ConfigFile(img, &v1.ConfigFile{
		OS:           "linux",
		Architecture: "arm64",
		Config:       v1.Config{Cmd: []string{"/hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(reg.tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	if reg.digest, err = img.Digest(); err != nil {
		t.Fatal(err)
	}
	reg.requests.Store(0)
	return reg
}

// captureLog returns what's logged until the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return &buf
}

// cacheFiles puts files of the given kinds for digest in c, as boot and
// save would have after pulling it.
func cacheFiles(t *testing.T, c *cache.Cache, digest v1.Hash, kinds ...string) {
	t.Helper()
	for _, kind := range kinds {
		if _, _, err := c.Get(kind, digest, func(w io.ReadWriteSeeker) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolvePulls(t *testing.T) {
	reg := newFlakyRegistry(t)
	c := cache.New(t.TempDir())
	captureLog(t)

	res, err := resolve(context.Background(), c, reg.tag, arm64, "images")
	if err != nil {
		t.Fatal(err)
	}
	if res.digest != reg.digest || res.img == nil {
		t.Errorf("resolve = %v, %v; want %v and the image", res.digest, res.img, reg.digest)
	}
	// The record waits until the caller has cached the files.
	if _, ok, _ := c.Ref(reg.tag, arm64); ok {
		t.Error("resolve recorded the tag before its files were cached")
	}
	if err := res.record(); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := c.Ref(reg.tag, arm64)
	if !ok || err != nil || rec.Digest != reg.digest {
		t.Errorf("record after a pull: %+v, %v, %v", rec, ok, err)
	}
}

func TestResolveFallback(t *testing.T) {
	for _, tt := range []struct {
		name string
		down func(*flakyRegistry)
	}{
		{"server error", func(reg *flakyRegistry) { reg.fail.Store("501 UNSUPPORTED") }},
		{"unreachable", func(reg *flakyRegistry) { reg.srv.Close() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := newFlakyRegistry(t)
			c := cache.New(t.TempDir())
			logs := captureLog(t)
			res, err := resolve(context.Background(), c, reg.tag, arm64, "images")
			if err != nil {
				t.Fatal(err)
			}
			cacheFiles(t, c, reg.digest, "images")
			if err := res.record(); err != nil {
				t.Fatal(err)
			}

			tt.down(reg)
			res, err = resolve(context.Background(), c, reg.tag, arm64, "images")
			if err != nil {
				t.Fatalf("resolve with the registry down: %v", err)
			}
			if res.digest != reg.digest || res.img != nil {
				t.Errorf("resolve = %v, %v; want %v from the cache", res.digest, res.img, reg.digest)
			}
			if !strings.Contains(logs.String(), "may be out of date") {
				t.Errorf("no warning in the log:\n%s", logs)
			}
		})
	}
}

func TestResolveNoFallback(t *testing.T) {
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	for _, tt := range []struct {
		name string
		fail string
		// record and cached say what a previous pull left in the cache.
		record, cached bool
		platform       v1.Platform
	}{
		{"unknown manifest", "404 MANIFEST_UNKNOWN", true, true, arm64},
		{"unauthorized", "401 UNAUTHORIZED", true, true, arm64},
		{"denied", "403 DENIED", true, true, arm64},
		{"no record", "501 UNSUPPORTED", false, true, arm64},
		{"not cached", "501 UNSUPPORTED", true, false, arm64},
		{"other platform", "501 UNSUPPORTED", true, true, amd64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := newFlakyRegistry(t)
			c := cache.New(t.TempDir())
			captureLog(t)
			if tt.record {
				if err := c.SetRef(reg.tag, arm64, reg.digest); err != nil {
					t.Fatal(err)
				}
			}
			if tt.cached {
				cacheFiles(t, c, reg.digest, "images")
			}

			reg.fail.Store(tt.fail)
			code := strings.Fields(tt.fail)[1]
			if _, err := resolve(context.Background(), c, reg.tag, tt.platform, "images"); err == nil || !strings.Contains(err.Error(), code) {
				t.Errorf("resolve = %v, want the registry's %s", err, code)
			}
		})
	}
}

// TestResolveCanceled checks a pull that was stopped doesn't fall back,
// though the tag's record and files are there.
func TestResolveCanceled(t *testing.T) {
	reg := newFlakyRegistry(t)
	c := cache.New(t.TempDir())
	captureLog(t)
	if err := c.SetRef(reg.tag, arm64, reg.digest); err != nil {
		t.Fatal(err)
	}
	cacheFiles(t, c, reg.digest, "images")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolve(ctx, c, reg.tag, arm64, "images"); err == nil {
		t.Error("resolve with a canceled context fell back to the cache")
	}
}

func TestResolveDigest(t *testing.T) {
	reg := newFlakyRegistry(t)
	c := cache.New(t.TempDir())
	captureLog(t)
	ref := reg.repo + "@" + reg.digest.String()

	// Not cached: it's pulled.
	res, err := resolve(context.Background(), c, ref, arm64, "images")
	if err != nil || res.digest != reg.digest || res.img == nil {
		t.Fatalf("resolve, nothing cached = %v, %v, %v; want the pulled image", res.digest, res.img, err)
	}

	// Cached: the registry isn't asked.
	cacheFiles(t, c, reg.digest, "images")
	reg.requests.Store(0)
	res, err = resolve(context.Background(), c, ref, arm64, "images")
	if err != nil || res.digest != reg.digest || res.img != nil {
		t.Errorf("resolve, cached = %v, %v, %v; want %v from the cache", res.digest, res.img, err, reg.digest)
	}
	if n := reg.requests.Load(); n != 0 {
		t.Errorf("resolve of a cached digest sent %d requests", n)
	}

	// A digest that isn't the manifest's, as an index's, is resolved
	// by its record.
	index := sha256Of(t, "an index")
	indexRef := reg.repo + "@" + index.String()
	if err := c.SetRef(indexRef, arm64, reg.digest); err != nil {
		t.Fatal(err)
	}
	res, err = resolve(context.Background(), c, indexRef, arm64, "images")
	if err != nil || res.digest != reg.digest || res.img != nil {
		t.Errorf("resolve of a recorded index = %v, %v, %v; want %v from the cache", res.digest, res.img, err, reg.digest)
	}
	if n := reg.requests.Load(); n != 0 {
		t.Errorf("resolve of a recorded index sent %d requests", n)
	}
}

// TestRecordAfterFiles checks that a pull that fails after the lookup,
// here fetching the layers, leaves the tag's record alone: it still
// points at files that are cached, which a boot can fall back to.
func TestRecordAfterFiles(t *testing.T) {
	for _, tt := range []struct {
		name     string
		platform v1.Platform
		kinds    []string
		pull     func(ref, cacheDir string) error
	}{
		{"image", arm64, []string{"images", "configs"}, func(ref, cacheDir string) error {
			_, _, err := pullImage(context.Background(), commonOptions{image: ref, platform: arm64, cacheDir: cacheDir})
			return err
		}},
		{"kernel", v1.Platform{OS: "linux", Architecture: runtime.GOARCH}, []string{"kernels"}, func(ref, cacheDir string) error {
			_, err := resolveKernel(context.Background(), ref, cacheDir)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := newFlakyRegistry(t)
			captureLog(t)
			cacheDir := t.TempDir()
			c := cache.New(cacheDir)
			old := sha256Of(t, "what the tag pointed at before")
			if err := c.SetRef(reg.tag, tt.platform, old); err != nil {
				t.Fatal(err)
			}
			cacheFiles(t, c, old, tt.kinds...)

			reg.failBlobs.Store(true)
			if err := tt.pull(reg.tag, cacheDir); err == nil {
				t.Fatal("pull succeeded with the blobs failing")
			}
			if rec, _, _ := c.Ref(reg.tag, tt.platform); rec.Digest != old {
				t.Errorf("record = %v after a failed pull, want %v", rec.Digest, old)
			}
		})
	}
}

// TestPullImageOffline checks boot has what it needs from the cache
// alone, with the registry down: the ext4 image and the image's config.
func TestPullImageOffline(t *testing.T) {
	reg := newFlakyRegistry(t)
	captureLog(t)
	opts := commonOptions{image: reg.tag, platform: arm64, cacheDir: t.TempDir()}

	config, path, err := pullImage(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	reg.srv.Close()
	offlineConfig, offlinePath, err := pullImage(context.Background(), opts)
	if err != nil {
		t.Fatalf("pullImage with the registry down: %v", err)
	}
	if offlinePath != path {
		t.Errorf("ext4 image at %s, want %s", offlinePath, path)
	}
	if len(offlineConfig.Cmd) != 1 || offlineConfig.Cmd[0] != "/hello" || len(config.Cmd) != 1 {
		t.Errorf("Cmd = %q online, %q offline; want [/hello]", config.Cmd, offlineConfig.Cmd)
	}
}

func sha256Of(t *testing.T, s string) v1.Hash {
	t.Helper()
	h, _, err := v1.SHA256(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
