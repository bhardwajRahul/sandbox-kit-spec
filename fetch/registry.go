package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"

	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// maxIndexDepth bounds how far a reference may nest indexes. Real
// artifacts use one level; anything deeper is malformed or hostile.
const maxIndexDepth = 4

// maxMetadataBytes bounds a manifest or index read. Metadata meets
// registry ceilings around 4 MB; a descriptor pointing at an enormous
// blob must become an error, not an allocation.
const maxMetadataBytes = 8 << 20

func defaultPlatform() ocispec.Platform {
	return ocispec.Platform{OS: "linux", Architecture: runtime.GOARCH}
}

func (c *Client) fetch(ctx context.Context, ref string) (*Kit, error) {
	repoName, referenceName, err := splitRef(ref)
	if err != nil {
		return nil, err
	}
	repo, err := c.repository(repoName)
	if err != nil {
		return nil, err
	}
	raw, dgst, err := c.readDescriptor(ctx, repo, referenceName)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, ErrNotAKit
	}
	d, err := spec.Decode(raw)
	if err != nil {
		return nil, err
	}
	// A fetched kit is published data. Decode only parses; the
	// published-form rules are what keep a hand-written annotation
	// from reaching resolve with kind: set, an unpinned kit, or a
	// build arg still in the document.
	if _, err := spec.ValidatePublished(raw, d); err != nil {
		return nil, err
	}
	return &Kit{
		Reference:  ref,
		Digest:     dgst,
		Descriptor: d,
		Raw:        append([]byte(nil), raw...),
	}, nil
}

func (c *Client) repository(repoName string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repoName)
	if err != nil {
		return nil, err
	}
	repo.PlainHTTP = c.plainHTTP || isLoopbackRegistry(repo.Reference.Registry)
	repo.Client = &auth.Client{
		Client:     &http.Client{Transport: retry.NewTransport(c.transport)},
		Cache:      c.cache,
		Credential: c.credential,
		Header:     http.Header{"User-Agent": []string{"sandbox-kit-fetch"}},
	}
	return repo, nil
}

// readDescriptor returns the descriptor annotation and the digest the
// reference itself resolved to.
//
// An index annotation is what a consumer reads first, and it is only an
// optimization: when it is absent the platform manifest is the contract.
// Either way the digest is the one the reference resolved to, so a lock
// pins the tag and not one platform's manifest.
func (c *Client) readDescriptor(ctx context.Context, repo *remote.Repository, referenceName string) ([]byte, string, error) {
	desc, body, err := fetchManifest(ctx, repo, referenceName)
	if err != nil {
		return nil, "", err
	}
	raw, _, ok, err := c.annotation(ctx, repo, desc, body, 0)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("index holds no manifest for %s", platforms.Format(c.platform))
	}
	return raw, desc.Digest.String(), nil
}

// annotation reads the descriptor annotation from desc.
//
// platform is the image manifest the annotation was read from, so a
// caller ranking several branches can keep the closer one. It is nil
// when the annotation came from the tagged index itself.
//
// ok is false when desc is an index that does not hold the wanted
// platform — a caller searching several nested indexes tries the next.
// An image manifest is ok even when its annotation is empty; that
// emptiness is "not a kit", not "look somewhere else".
func (c *Client) annotation(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, body []byte, depth int) ([]byte, *ocispec.Platform, bool, error) {
	if !isIndex(desc.MediaType) {
		if !isImageManifest(desc.MediaType) {
			return nil, nil, false, fmt.Errorf("a kit is a plain image manifest, not %q", desc.MediaType)
		}
		var manifest ocispec.Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return nil, nil, false, fmt.Errorf("parse manifest: %w", err)
		}
		if err := requirePlainImageManifest(desc, manifest); err != nil {
			return nil, nil, false, err
		}
		return []byte(manifest.Annotations[spec.AnnotationDescriptor]), desc.Platform, true, nil
	}
	if depth > maxIndexDepth {
		return nil, nil, false, fmt.Errorf("indexes nest deeper than any kit should")
	}
	var index ocispec.Index
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, nil, false, fmt.Errorf("parse index: %w", err)
	}
	if index.SchemaVersion != 2 {
		return nil, nil, false, fmt.Errorf("an index declares schemaVersion 2, got %d", index.SchemaVersion)
	}
	// Only the tagged index's annotation counts. A nested index is not
	// what the reference resolved to, and adopting its annotation would
	// hide an absent one on the index a consumer actually reads.
	if depth == 0 {
		if raw := index.Annotations[spec.AnnotationDescriptor]; raw != "" {
			return []byte(raw), nil, true, nil
		}
	}

	child, nested := chooseManifest(&index, c.platform)
	matcher := platforms.Only(c.platform)
	var bestRaw []byte
	var bestPlat *ocispec.Platform
	found := false
	consider := func(ann []byte, plat *ocispec.Platform) {
		switch {
		case !found:
			bestRaw, bestPlat, found = ann, plat, true
		case plat != nil && (bestPlat == nil || matcher.Less(*plat, *bestPlat)):
			bestRaw, bestPlat = ann, plat
		}
	}
	if child != nil {
		body, err := fetchDescriptor(ctx, repo, *child)
		if err != nil {
			return nil, nil, false, err
		}
		ann, plat, ok, err := c.annotation(ctx, repo, *child, body, depth+1)
		if err != nil {
			return nil, nil, false, err
		}
		if ok {
			consider(ann, plat)
			// Already the platform that was asked for. A nested
			// branch cannot be a closer match, and a manifest at
			// this level is preferred to one buried in a branch.
			if plat != nil && !matcher.Less(c.platform, *plat) {
				return ann, plat, true, nil
			}
		}
	}
	for _, n := range nested {
		body, err := fetchDescriptor(ctx, repo, n)
		if err != nil {
			return nil, nil, false, err
		}
		ann, plat, ok, err := c.annotation(ctx, repo, n, body, depth+1)
		if err != nil {
			return nil, nil, false, err
		}
		if ok {
			consider(ann, plat)
		}
	}
	if !found {
		return nil, nil, false, nil
	}
	return bestRaw, bestPlat, true, nil
}

// requirePlainImageManifest enforces the artifact shape the spec names:
// a plain image manifest — no artifactType — with an image-config blob.
// An OCI artifact unmarshals into the same struct.
func requirePlainImageManifest(desc ocispec.Descriptor, m ocispec.Manifest) error {
	if m.SchemaVersion != 2 {
		return fmt.Errorf("a manifest declares schemaVersion 2, got %d", m.SchemaVersion)
	}
	mediaType := desc.MediaType
	if mediaType == "" {
		mediaType = m.MediaType
	}
	if !isImageManifest(mediaType) {
		return fmt.Errorf("a kit is a plain image manifest, not %q", mediaType)
	}
	if m.ArtifactType != "" {
		return fmt.Errorf("a kit sets no artifactType, got %q", m.ArtifactType)
	}
	if m.Config.MediaType != ocispec.MediaTypeImageConfig &&
		m.Config.MediaType != "application/vnd.docker.container.image.v1+json" {
		return fmt.Errorf("a kit's config is an image config, not %q", m.Config.MediaType)
	}
	return nil
}

// chooseManifest prefers an image manifest for want. A single runnable
// manifest is accepted without a platform match, because a one-platform
// index has nothing else to be. Several that miss the platform are not
// guessed between.
func chooseManifest(index *ocispec.Index, want ocispec.Platform) (matched *ocispec.Descriptor, nested []ocispec.Descriptor) {
	matcher := platforms.Only(want)
	var only *ocispec.Descriptor
	runnable := 0
	for i := range index.Manifests {
		m := &index.Manifests[i]
		if m.Platform != nil && m.Platform.OS == "unknown" {
			continue
		}
		if isIndex(m.MediaType) {
			nested = append(nested, *m)
			continue
		}
		if m.MediaType != "" && !isImageManifest(m.MediaType) {
			continue
		}
		runnable++
		if only == nil {
			only = m
		}
		if m.Platform != nil && matcher.Match(*m.Platform) {
			// Only matches compatible platforms too — amd64 matches
			// 386 — and Less ranks the closer one ahead. Keeping the
			// last match would let index order pick the worse one.
			if matched == nil || matcher.Less(*m.Platform, *matched.Platform) {
				matched = m
			}
		}
	}
	if matched != nil {
		// 386 satisfies an amd64 request, and returning it here would
		// hide an amd64 manifest inside a nested index. A match that
		// is already the platform asked for does not need that search.
		if len(nested) == 0 || !matcher.Less(want, *matched.Platform) {
			return matched, nil
		}
		return matched, nested
	}
	// A platform-less sole manifest has nothing to match against. One
	// that names a different platform is not a guess we get to make.
	if runnable == 1 && only.Platform == nil {
		return only, nil
	}
	return nil, nested
}

func fetchManifest(ctx context.Context, repo *remote.Repository, referenceName string) (ocispec.Descriptor, []byte, error) {
	desc, rc, err := repo.FetchReference(ctx, referenceName)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	body, err := readLimited(rc, desc.Digest.String())
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	return desc, body, nil
}

func fetchDescriptor(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := repo.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	return readLimited(rc, desc.Digest.String())
}

func readLimited(rc io.ReadCloser, name string) ([]byte, error) {
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(body) > maxMetadataBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes; no kit metadata blob is that large", name, maxMetadataBytes)
	}
	return body, nil
}

func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex ||
		mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func isImageManifest(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageManifest ||
		mediaType == "application/vnd.docker.distribution.manifest.v2+json"
}

// splitRef normalizes a reference, then separates the repository from
// the tag or digest. A bare name means latest, which is what it means
// to docker pull.
func splitRef(ref string) (repo, referenceName string, err error) {
	named, err := parseNamed(ref)
	if err != nil {
		return "", "", err
	}
	switch t := named.(type) {
	case reference.Canonical:
		// A digest answers for a reference carrying both: resolving the
		// tag instead would read whatever it points at now.
		return reference.TrimNamed(named).String(), t.Digest().String(), nil
	case reference.Tagged:
		return reference.TrimNamed(named).String(), t.Tag(), nil
	default:
		return "", "", fmt.Errorf("reference %q names no tag or digest", ref)
	}
}

func parseNamed(ref string) (reference.Named, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, fmt.Errorf("reference %q: %w", ref, err)
	}
	return reference.TagNameOnly(named), nil
}

func withDigest(named reference.Named, dgst digest.Digest) (string, error) {
	canonical, err := reference.WithDigest(reference.TrimNamed(named), dgst)
	if err != nil {
		return "", err
	}
	return canonical.String(), nil
}

func isLoopbackRegistry(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if name == "localhost" {
		return true
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}
