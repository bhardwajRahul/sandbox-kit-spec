package fetch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
)

// LoadImage implements assemble.ImageLoader using this client's registry
// credentials and selected platform. It reads manifests and the config blob,
// verifies their sizes and digests, and never downloads filesystem layers.
// Pass the pinned image references in Resolved.Kits to avoid tag movement
// between descriptor resolution and image assembly.
func (c *Client) LoadImage(ctx context.Context, ref string) (assemble.Input, error) {
	input, err := c.loadImage(ctx, ref)
	if err != nil {
		return assemble.Input{}, fmt.Errorf("load image %s: %w", ref, err)
	}
	return input, nil
}

func (c *Client) loadImage(ctx context.Context, ref string) (assemble.Input, error) {
	repoName, name, err := splitRef(ref)
	if err != nil {
		return assemble.Input{}, err
	}
	repo, err := c.repository(repoName)
	if err != nil {
		return assemble.Input{}, err
	}
	budget := &manifestBudget{}
	desc, body, err := fetchManifest(ctx, repo, name, budget)
	if err != nil {
		return assemble.Input{}, err
	}
	raw, selectedPlatform, found, err := c.metadata(ctx, repo, desc, body, 0, budget, false)
	if err != nil {
		return assemble.Input{}, err
	}
	if !found {
		return assemble.Input{}, fmt.Errorf("index holds no manifest for %s", platforms.Format(c.platform))
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return assemble.Input{}, fmt.Errorf("parse image manifest: %w", err)
	}
	if manifest.Config.Size < 0 || manifest.Config.Size > maxMetadataBytes {
		return assemble.Input{}, fmt.Errorf("image config size %d is outside the metadata budget", manifest.Config.Size)
	}
	rc, err := repo.Fetch(ctx, manifest.Config)
	if err != nil {
		return assemble.Input{}, fmt.Errorf("fetch image config: %w", err)
	}
	configRaw, err := readVerified(rc, manifest.Config)
	if err != nil {
		return assemble.Input{}, err
	}
	var config ocispec.Image
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return assemble.Input{}, fmt.Errorf("parse image config: %w", err)
	}
	if selectedPlatform != nil && !platforms.OnlyStrict(*selectedPlatform).Match(config.Platform) {
		return assemble.Input{}, fmt.Errorf("image config platform %s disagrees with manifest platform %s",
			platforms.Format(config.Platform), platforms.Format(*selectedPlatform))
	}
	if !platforms.Only(c.platform).Match(config.Platform) {
		return assemble.Input{}, fmt.Errorf("image config is %s, requested %s", platforms.Format(config.Platform), platforms.Format(c.platform))
	}
	if config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) {
		return assemble.Input{}, fmt.Errorf("image config rootfs does not match manifest layers")
	}
	return assemble.Input{Name: ref, Manifest: manifest, Config: config}, nil
}
