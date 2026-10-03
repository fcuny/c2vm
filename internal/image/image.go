// Package image pulls container images through containerd and unpacks
// them into a directory.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/platforms"
	"github.com/moby/go-archive"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Image is a container image that has been pulled into containerd's
// content store.
type Image struct {
	client   *client.Client
	image    client.Image
	platform platforms.MatchComparer
}

// Pull fetches ref for the given platform.
func Pull(ctx context.Context, c *client.Client, ref string, platform platforms.MatchComparer) (*Image, error) {
	image, err := c.Pull(ctx, ref, client.WithPlatformMatcher(platform))
	if err != nil {
		return nil, fmt.Errorf("failed to pull the container %s: %w", ref, err)
	}

	imageSize, err := image.Usage(ctx, client.WithUsageManifestLimit(1))
	if err != nil {
		return nil, fmt.Errorf("failed to get the size of the image: %w", err)
	}

	log.Printf("pulled %s (%d bytes)\n", image.Name(), imageSize)

	return &Image{client: c, image: image, platform: platform}, nil
}

// Unpack extracts the image's layers, in order, into dir.
func (i *Image) Unpack(ctx context.Context, dir string) error {
	manifest, err := images.Manifest(ctx, i.client.ContentStore(), i.image.Target(), i.platform)
	if err != nil {
		return fmt.Errorf("failed to get the manifest: %w", err)
	}

	for _, desc := range manifest.Layers {
		log.Printf("extracting layer %s\n", desc.Digest.String())
		if err := i.unpackLayer(ctx, desc, dir); err != nil {
			return fmt.Errorf("layer %s: %w", desc.Digest, err)
		}
	}

	return nil
}

func (i *Image) unpackLayer(ctx context.Context, desc ocispec.Descriptor, dir string) error {
	layer, err := i.client.ContentStore().ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer layer.Close()

	return archive.Untar(content.NewReader(layer), dir, &archive.TarOptions{NoLchown: true})
}

// Config returns the image's runtime configuration: its entrypoint,
// command, environment, working directory and so on.
func (i *Image) Config(ctx context.Context) (ocispec.ImageConfig, error) {
	config, err := images.Config(ctx, i.client.ContentStore(), i.image.Target(), i.platform)
	if err != nil {
		return ocispec.ImageConfig{}, err
	}

	configBlob, err := content.ReadBlob(ctx, i.client.ContentStore(), config)
	if err != nil {
		return ocispec.ImageConfig{}, err
	}

	var imageSpec ocispec.Image
	if err := json.Unmarshal(configBlob, &imageSpec); err != nil {
		return ocispec.ImageConfig{}, fmt.Errorf("failed to parse the image config: %w", err)
	}

	return imageSpec.Config, nil
}
