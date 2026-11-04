// Package image pulls container images from registries and writes
// their filesystem as an ext4 image.
package image

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Image is a container image in a registry. Its layers are fetched when
// it's written out.
type Image struct {
	ref   name.Reference
	image v1.Image
}

// Pull resolves ref for the given platform. ref can be a short name, as
// with docker: "alpine" is docker.io/library/alpine:latest.
// Credentials come from the docker and podman configurations, as with
// `docker login` or `podman login`.
func Pull(ctx context.Context, ref string, platform v1.Platform) (*Image, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference %q: %w", ref, err)
	}

	img, err := remote.Image(r,
		remote.WithContext(ctx),
		remote.WithPlatform(platform),
		remote.WithAuthFromKeychain(anonymousFallback{authn.DefaultKeychain}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pull %s: %w", r, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, err
	}
	log.Printf("resolved %s to %s\n", r.Name(), digest)

	return &Image{ref: r, image: img}, nil
}

// anonymousFallback pulls anonymously when credentials can't be looked
// up, e.g. when the docker configuration names a credential helper that
// isn't installed, so public images still work.
type anonymousFallback struct {
	authn.Keychain
}

func (k anonymousFallback) Resolve(r authn.Resource) (authn.Authenticator, error) {
	auth, err := k.Keychain.Resolve(r)
	if err != nil {
		log.Printf("warning: can't look up credentials for %s, pulling anonymously: %v\n", r.RegistryStr(), err)
		return authn.Anonymous, nil
	}
	return auth, nil
}

// Transient reports whether err, from Pull, says nothing about the image
// itself: the registry couldn't be reached, refused the request for now
// (429), or failed (5xx). Any other answer from the registry, like an
// unknown manifest or a denied request, is about the image. A canceled
// pull isn't transient either.
func Transient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusTooManyRequests || terr.StatusCode >= 500
	}
	var nerr net.Error
	return errors.As(err, &nerr)
}

// Digest is the digest of the image's manifest, for the platform it
// was pulled for.
func (i *Image) Digest() (v1.Hash, error) {
	return i.image.Digest()
}

// RawConfig returns the image's configuration file, as the registry
// serves it, for ParseConfig.
func (i *Image) RawConfig() ([]byte, error) {
	raw, err := i.image.RawConfigFile()
	if err != nil {
		return nil, fmt.Errorf("failed to read the image config: %w", err)
	}
	return raw, nil
}

// ParseConfig returns the runtime configuration in an image's
// configuration file: its entrypoint, command, environment, working
// directory and so on.
func ParseConfig(raw []byte) (ocispec.ImageConfig, error) {
	cf, err := v1.ParseConfigFile(bytes.NewReader(raw))
	if err != nil {
		return ocispec.ImageConfig{}, fmt.Errorf("failed to parse the image config: %w", err)
	}

	return ocispec.ImageConfig{
		User:       cf.Config.User,
		Env:        cf.Config.Env,
		Entrypoint: cf.Config.Entrypoint,
		Cmd:        cf.Config.Cmd,
		WorkingDir: cf.Config.WorkingDir,
	}, nil
}

// WriteExt4 writes the image's filesystem, with its layers applied in
// order, to w as an ext4 filesystem. The filesystem is compact: it has
// no free space, and is meant to be mounted read-only.
func (i *Image) WriteExt4(w io.ReadWriteSeeker) error {
	rc := mutate.Extract(i.image)
	defer rc.Close()

	if err := tar2ext4.Convert(rc, w); err != nil {
		return fmt.Errorf("failed to convert %s to ext4: %w", i.ref, err)
	}
	return nil
}

// ExtractFile copies the file at name in the image's filesystem to w.
func (i *Image) ExtractFile(name string, w io.Writer) error {
	rc := mutate.Extract(i.image)
	defer rc.Close()

	want := path.Clean(path.Join("/", name))
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s has no %s", i.ref, want)
		}
		if err != nil {
			return err
		}
		if path.Clean(path.Join("/", hdr.Name)) != want {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s in %s is not a regular file", want, i.ref)
		}
		_, err = io.Copy(w, tr)
		return err
	}
}
