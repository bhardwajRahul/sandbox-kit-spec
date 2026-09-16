// Package assemble is the manifest arithmetic of kit composition: given the
// sandbox kit's image and the mixins' overlay images, in composition order,
// it computes the merged image's config and manifest. No filesystem work
// happens — every layer already exists as a blob wherever the inputs live;
// assembly writes two small JSON blobs that reference them.
package assemble

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Input is one kit image's platform manifest and decoded config.
type Input struct {
	// Name identifies the kit in collision and validation errors —
	// typically the reference it was consumed by.
	Name     string
	Manifest ocispec.Manifest
	Config   ocispec.Image
}

// Merged is the composed image: the sandbox's config with every mixin's
// layers appended in composition order.
type Merged struct {
	Manifest   ocispec.Manifest
	Config     ocispec.Image
	ConfigJSON []byte
}

// Merge computes the composed image. The sandbox anchors the runtime
// contract — entrypoint, cmd, user, working dir travel unchanged, and the
// same fields in a mixin's config are ignored (they exist for standalone
// `docker run` of the mixin) — while each mixin contributes its layers
// plus its additive config: manifest layers and config diff_ids
// concatenate in composition order, and ENV, LABEL, EXPOSE, and VOLUME
// entries from the mixin's config merge into the sandbox's (the frontend
// records only what the mixin's recipe stated over its base). PATH appends
// elements instead of substituting; any other env var two kits set to
// different values is a conflict, the config analogue of the file
// collisions Validate rejects. Labels resolve to the first writer instead:
// they are metadata no runtime reads, and build tooling stamps its own
// values into every image.
func Merge(sandbox Input, mixins []Input) (*Merged, error) {
	if len(sandbox.Manifest.Layers) != len(sandbox.Config.RootFS.DiffIDs) {
		return nil, fmt.Errorf("sandbox kit %s: %d manifest layers but %d config diff_ids", sandbox.Name, len(sandbox.Manifest.Layers), len(sandbox.Config.RootFS.DiffIDs))
	}

	config := sandbox.Config
	config.RootFS.DiffIDs = append([]godigest.Digest{}, sandbox.Config.RootFS.DiffIDs...)
	config.History = append([]ocispec.History{}, sandbox.Config.History...)

	manifest := ocispec.Manifest{
		Versioned:   sandbox.Manifest.Versioned,
		MediaType:   ocispec.MediaTypeImageManifest,
		Layers:      append([]ocispec.Descriptor{}, sandbox.Manifest.Layers...),
		Annotations: map[string]string{},
	}

	owners := newConfigOwners(sandbox)
	for _, m := range mixins {
		if len(m.Manifest.Layers) != len(m.Config.RootFS.DiffIDs) {
			return nil, fmt.Errorf("mixin kit %s: %d manifest layers but %d config diff_ids", m.Name, len(m.Manifest.Layers), len(m.Config.RootFS.DiffIDs))
		}
		manifest.Layers = append(manifest.Layers, m.Manifest.Layers...)
		config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, m.Config.RootFS.DiffIDs...)
		for range m.Manifest.Layers {
			// No timestamp: the merged config is addressed by the lock, and
			// callers rely on one lock yielding one digest so sandboxes
			// sharing a lock share an image record. A wall clock would give
			// every assembly of the same set a different digest.
			config.History = append(config.History, ocispec.History{
				CreatedBy: "runtime-kit assemble " + m.Name,
				Comment:   "kit overlay layer",
			})
		}
		if err := owners.mergeConfig(&config.Config, m); err != nil {
			return nil, err
		}
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal merged config: %w", err)
	}
	manifest.Config = ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    godigest.FromBytes(configJSON),
		Size:      int64(len(configJSON)),
	}

	return &Merged{Manifest: manifest, Config: config, ConfigJSON: configJSON}, nil
}

// configOwners tracks which kit stated each env and label value, so an
// env conflict names both parties instead of silently letting composition
// order pick a winner, and so a label keeps its first writer.
type configOwners struct {
	env    map[string]valueOwner
	labels map[string]valueOwner
}

type valueOwner struct {
	value string
	kit   string
}

func newConfigOwners(sandbox Input) *configOwners {
	o := &configOwners{env: map[string]valueOwner{}, labels: map[string]valueOwner{}}
	for _, kv := range sandbox.Config.Config.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			o.env[k] = valueOwner{value: v, kit: sandbox.Name}
		}
	}
	for k, v := range sandbox.Config.Config.Labels {
		o.labels[k] = valueOwner{value: v, kit: sandbox.Name}
	}
	return o
}

// mergeConfig folds one mixin's config delta into the composed config.
// PATH is additive by construction: the mixin's PATH value holds only the
// elements its recipe added (the frontend's delta), and they append to the
// composed PATH, deduplicated. Every other env var is first-writer-owned
// and a second kit may restate the same value, never a different one.
// EXPOSE and VOLUME are sets, so their union needs no arbitration.
func (o *configOwners) mergeConfig(config *ocispec.ImageConfig, m Input) error {
	for _, kv := range m.Config.Config.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == "PATH" {
			mergePath(config, o, m.Name, v)
			continue
		}
		if prev, exists := o.env[k]; exists {
			if prev.value != v {
				return fmt.Errorf("env conflict on %s: %s sets %q but %s sets %q", k, prev.kit, prev.value, m.Name, v)
			}
			continue
		}
		o.env[k] = valueOwner{value: v, kit: m.Name}
		config.Env = append(config.Env, kv)
	}

	// Labels are inert metadata on an image that is never published, so a
	// disagreement resolves to the first writer rather than failing the
	// composition. Build tooling guarantees disagreement — buildx stamps
	// every image with its own recipe path in
	// com.docker.image.source.entrypoint — and no runtime behavior hangs
	// on the value, which is what separates this from env.
	for k, v := range m.Config.Config.Labels {
		if _, exists := o.labels[k]; exists {
			continue
		}
		o.labels[k] = valueOwner{value: v, kit: m.Name}
		if config.Labels == nil {
			config.Labels = map[string]string{}
		}
		config.Labels[k] = v
	}

	for k := range m.Config.Config.ExposedPorts {
		if config.ExposedPorts == nil {
			config.ExposedPorts = map[string]struct{}{}
		}
		config.ExposedPorts[k] = struct{}{}
	}
	for k := range m.Config.Config.Volumes {
		if config.Volumes == nil {
			config.Volumes = map[string]struct{}{}
		}
		config.Volumes[k] = struct{}{}
	}
	return nil
}

func mergePath(config *ocispec.ImageConfig, o *configOwners, kit, addition string) {
	current := ""
	if prev, exists := o.env["PATH"]; exists {
		current = prev.value
	}
	have := map[string]bool{}
	for _, e := range strings.Split(current, ":") {
		have[e] = true
	}
	merged := current
	for _, e := range strings.Split(addition, ":") {
		if e == "" || have[e] {
			continue
		}
		have[e] = true
		if merged == "" {
			merged = e
		} else {
			merged += ":" + e
		}
	}
	if merged == current && current != "" {
		return
	}
	o.env["PATH"] = valueOwner{value: merged, kit: kit}
	replaced := false
	for i, kv := range config.Env {
		if strings.HasPrefix(kv, "PATH=") {
			config.Env[i] = "PATH=" + merged
			replaced = true
			break
		}
	}
	if !replaced {
		config.Env = append(config.Env, "PATH="+merged)
	}
}

// TagRepository is the repository every assembled-image tag lives under;
// the runtime's GC recognizes retired assemblies by it.
const TagRepository = "runtime-kit-assembled"

// Tag derives the deterministic local tag for an assembled image from the
// bytes that define it — the lock. Two sandboxes created from the same lock
// name the same image and share its record; any change in the set, its
// digests, or its argument values names a different one.
func Tag(lockJSON []byte) string {
	sum := sha256.Sum256(lockJSON)
	return TagRepository + ":" + hex.EncodeToString(sum[:])[:16]
}
