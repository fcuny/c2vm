package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"fcuny.net/c2vm/internal/cache"
	"fcuny.net/c2vm/internal/image"
)

// resolved is what resolve resolved a reference to.
type resolved struct {
	digest v1.Hash
	// img is the image pulled from the registry, or nil if its files are
	// cached and the registry wasn't needed.
	img *image.Image
	// record records what the reference resolved to, for when a later
	// lookup fails. Call it once the image's files are cached, so that a
	// record never points at files that aren't.
	record func() error
}

// resolve resolves ref to the digest of its manifest for platform, and
// returns the image it pulled. It returns a nil image, without pulling,
// when the files of the given kinds are in c for the digest it resolves
// to. It skips the registry entirely when:
//
//   - ref is a digest, which names the same image forever;
//   - or the registry couldn't answer (see image.Transient), and ref is
//     what it last resolved to. That may be out of date, so it says so.
//
// Any other failure is returned as is.
func resolve(ctx context.Context, c *cache.Cache, ref string, platform v1.Platform, kinds ...string) (resolved, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return resolved{}, fmt.Errorf("invalid image reference %q: %w", ref, err)
	}
	fromCache := func(digest v1.Hash) resolved {
		return resolved{digest: digest, record: func() error { return nil }}
	}
	cached := func(digest v1.Hash) bool {
		for _, kind := range kinds {
			if !c.Has(kind, digest) {
				return false
			}
		}
		return true
	}

	if d, ok := r.(name.Digest); ok {
		// The digest of a manifest for the platform, or of an index of
		// them, which only a record can resolve.
		if digest, err := v1.NewHash(d.DigestStr()); err == nil && cached(digest) {
			log.Printf("using cached %s\n", r.Name())
			return fromCache(digest), nil
		}
		rec, ok, err := c.Ref(r.Name(), platform)
		if err != nil {
			return resolved{}, err
		}
		if ok && cached(rec.Digest) {
			log.Printf("resolved %s to %s, from the cache\n", r.Name(), rec.Digest)
			return fromCache(rec.Digest), nil
		}
	}

	res, pullErr := fromRegistry(ctx, r, platform, cached)
	if pullErr != nil {
		// A pull that was stopped isn't the registry's doing.
		if ctx.Err() != nil || !image.Transient(pullErr) {
			return resolved{}, pullErr
		}
		rec, ok, err := c.Ref(r.Name(), platform)
		if err != nil || !ok || !cached(rec.Digest) {
			return resolved{}, pullErr
		}
		log.Printf("warning: %v\n", pullErr)
		log.Printf("using %s, which %s resolved to on %s: it may be out of date\n", rec.Digest, r.Name(), rec.Resolved.Format(time.DateOnly))
		return fromCache(rec.Digest), nil
	}
	res.record = func() error { return c.SetRef(r.Name(), platform, res.digest) }
	return res, nil
}

// lookupTimeout bounds the lookup of a reference's digest. Without it,
// a registry that accepts the connection and then never answers would
// hang the boot, even with the image cached.
var lookupTimeout = 30 * time.Second

// fromRegistry looks r up within lookupTimeout, and pulls the digest it
// resolves to unless cached says its files are there. The pull keeps
// ctx, with no deadline, as a first pull can take long.
func fromRegistry(ctx context.Context, r name.Reference, platform v1.Platform, cached func(v1.Hash) bool) (resolved, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	digest, err := image.Lookup(lookupCtx, r.Name(), platform)
	if err != nil {
		if ctx.Err() == nil && lookupCtx.Err() != nil {
			err = fmt.Errorf("%s didn't answer within %s: %w", r.Context().RegistryStr(), lookupTimeout, err)
		}
		return resolved{}, err
	}
	if cached(digest) {
		return resolved{digest: digest}, nil
	}

	img, err := image.Pull(ctx, r.Context().Digest(digest.String()).Name(), platform)
	if err != nil {
		return resolved{}, err
	}
	return resolved{digest: digest, img: img}, nil
}

// fromImage returns a cache.Get create function that writes a file from
// img, the image resolve returned. When that's nil, resolve found the
// file cached, so it's only called if the file was removed since.
func fromImage(img *image.Image, write func(*image.Image, io.ReadWriteSeeker) error) func(io.ReadWriteSeeker) error {
	return func(w io.ReadWriteSeeker) error {
		if img == nil {
			return errors.New("it was removed from the cache while c2vm used it")
		}
		return write(img, w)
	}
}
