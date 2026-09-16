package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/docker/sandbox-kit-spec/assemble"
)

// maxIndexDepth bounds how far a reference may nest indexes. Real
// artifacts use one level; anything deeper is malformed or hostile.
const maxIndexDepth = 4

// fetcher is the part of an oras target these checks need.
type fetcher interface {
	Resolve(context.Context, string) (ocispec.Descriptor, error)
	Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error)
}

// RegistryOption configures how a registry source reaches the registry.
type RegistryOption func(*registryOptions)

type registryOptions struct{ plainHTTP bool }

// WithPlainHTTP reaches the registry over HTTP instead of HTTPS.
//
// Needed for a registry that serves no TLS, which in practice means one
// standing in for a real one: the throwaway `registry:2` a test or a
// local build loop pushes to. A loopback registry gets this without
// asking ([resolveOptions]); this is for the rest, and stating it is
// how a caller takes responsibility for a request that crosses a
// network in the clear.
func WithPlainHTTP() RegistryOption {
	return func(o *registryOptions) { o.plainHTTP = true }
}

func resolveOptions(opts []RegistryOption) registryOptions {
	var o registryOptions
	for _, apply := range opts {
		apply(&o)
	}
	return o
}

// FromRegistry reads a published kit from a registry. This is the source
// that sees what the registry actually serves for a tag, which no
// build-time check can.
func FromRegistry(ctx context.Context, ref string, opts ...RegistryOption) (Artifact, error) {
	repo, tag, err := splitRef(ref)
	if err != nil {
		return nil, err
	}
	o := resolveOptions(opts)
	r, err := remoteRepository(repo, o)
	if err != nil {
		return nil, fmt.Errorf("reference %q: %w", ref, err)
	}
	return resolveArtifact(ctx, r, tag, &o, r.Reference.Registry)
}

// FromRegistryAll returns one artifact per runnable platform. The rules in
// §9 and §10 apply to every platform manifest, so judging only this host's
// would report a multi-platform artifact conforming on the strength of one
// of its images.
func FromRegistryAll(ctx context.Context, ref string, opts ...RegistryOption) ([]Artifact, error) {
	repo, tag, err := splitRef(ref)
	if err != nil {
		return nil, err
	}
	o := resolveOptions(opts)
	r, err := remoteRepository(repo, o)
	if err != nil {
		return nil, fmt.Errorf("reference %q: %w", ref, err)
	}
	return resolveAll(ctx, r, tag, &o, r.Reference.Registry)
}

// FromLayoutAll returns one artifact per runnable platform in a layout.
func FromLayoutAll(ctx context.Context, dir, tag string) ([]Artifact, error) {
	store, err := oci.NewFromFS(ctx, dirFS(dir))
	if err != nil {
		return nil, fmt.Errorf("open layout %s: %w", dir, err)
	}
	return resolveAll(ctx, store, tag, nil, "")
}

// resolveAll collects every runnable platform manifest under a reference,
// keeping the index's annotations on each so the index checks still apply.
func resolveAll(ctx context.Context, f fetcher, tag string, reach *registryOptions, origin string) ([]Artifact, error) {
	desc, err := f.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", tag, err)
	}
	return resolveFrom(ctx, f, desc, tag, nil, 0, reach, origin)
}

// resolveFrom collects the platform manifests under desc, carrying the
// annotations of the OUTERMOST index down. That index is the one a
// registry serves for the tag, so it is the one whose annotations a
// consumer reads first; starting fresh at a nested index would leave the
// visible one unchecked.
func resolveFrom(ctx context.Context, f fetcher, desc ocispec.Descriptor, tag string, outer map[string]string, depth int, reach *registryOptions, origin string) ([]Artifact, error) {
	// The same bound the single-artifact resolver applies: a layout is
	// untrusted input, and an arbitrarily deep chain of indexes would
	// otherwise recurse and fetch until something gives out.
	if depth > maxIndexDepth {
		return nil, fmt.Errorf("%s: indexes nest deeper than any kit should", tag)
	}
	body, err := fetchAll(ctx, f, desc)
	if err != nil {
		return nil, err
	}
	if !isIndex(desc.MediaType) {
		var m ocispec.Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if err := requirePlainImageManifest(desc, m); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		return []Artifact{&ociArtifact{fetcher: f, manifest: m, reach: reach, origin: origin}}, nil
	}

	var index ocispec.Index
	if err := json.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if index.SchemaVersion != 2 {
		return nil, fmt.Errorf("%s: an index declares schemaVersion 2, got %d", tag, index.SchemaVersion)
	}
	// Captured at depth zero only: the tagged index is the one a consumer
	// reads first, and when it has no annotations that absence is the
	// finding — adopting a nested index's set would validate the wrong
	// index and suppress the missing-annotation warning.
	if depth == 0 {
		outer = index.Annotations
	}
	var out []Artifact
	for _, child := range index.Manifests {
		if p := child.Platform; p != nil && p.OS == "unknown" {
			continue
		}
		if isIndex(child.MediaType) {
			nested, err := resolveFrom(ctx, f, child, tag, outer, depth+1, reach, origin)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			continue
		}
		childBody, err := fetchAll(ctx, f, child)
		if err != nil {
			return nil, err
		}
		var m ocispec.Manifest
		if err := json.Unmarshal(childBody, &m); err != nil {
			return nil, fmt.Errorf("parse platform manifest: %w", err)
		}
		if err := requirePlainImageManifest(child, m); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		out = append(out, &ociArtifact{
			fetcher:          f,
			reach:            reach,
			origin:           origin,
			manifest:         m,
			indexAnnotations: outer,
			hasIndex:         true,
			platform:         child.Platform,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: index holds no runnable platform manifest", tag)
	}
	return out, nil
}

// FromLayout reads a kit from an OCI layout directory, the shape
// `buildx --output type=oci` writes, so a build can be checked without a
// registry.
func FromLayout(ctx context.Context, dir, tag string) (Artifact, error) {
	store, err := oci.NewFromFS(ctx, dirFS(dir))
	if err != nil {
		return nil, fmt.Errorf("open layout %s: %w", dir, err)
	}
	return resolveArtifact(ctx, store, tag, nil, "")
}

// splitRef separates the repository from the tag or digest.
func splitRef(ref string) (string, string, error) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:], nil
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return "", "", fmt.Errorf("reference %q names no tag or digest", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// resolveArtifact descends to the image manifest a consumer would run,
// keeping the index's annotations when one fronts it.
func resolveArtifact(ctx context.Context, f fetcher, tag string, reach *registryOptions, origin string) (Artifact, error) {
	return resolveArtifactFor(ctx, f, tag, reach, origin, nil)
}

// resolveArtifactFor resolves one artifact, preferring a platform when
// the caller is judging a particular one.
func resolveArtifactFor(ctx context.Context, f fetcher, tag string, reach *registryOptions, origin string, want *ocispec.Platform) (Artifact, error) {
	desc, err := f.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", tag, err)
	}
	a := &ociArtifact{fetcher: f, reach: reach, origin: origin}

	for depth := 0; ; depth++ {
		body, err := fetchAll(ctx, f, desc)
		if err != nil {
			return nil, err
		}
		if !isIndex(desc.MediaType) {
			if err := json.Unmarshal(body, &a.manifest); err != nil {
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			if err := requirePlainImageManifest(desc, a.manifest); err != nil {
				return nil, fmt.Errorf("%s: %w", tag, err)
			}
			return a, nil
		}
		if depth > maxIndexDepth {
			return nil, fmt.Errorf("%s: indexes nest deeper than any kit should", tag)
		}
		var index ocispec.Index
		if err := json.Unmarshal(body, &index); err != nil {
			return nil, fmt.Errorf("parse index: %w", err)
		}
		if index.SchemaVersion != 2 {
			return nil, fmt.Errorf("%s: an index declares schemaVersion 2, got %d", tag, index.SchemaVersion)
		}
		if !a.hasIndex {
			a.indexAnnotations, a.hasIndex = index.Annotations, true
		}
		// A wanted platform is searched for rather than walked
		// towards: an index may hold several platform-less nested
		// indexes, and committing to the first would miss a platform
		// that is only in a later one.
		if want != nil {
			child, err := findPlatformManifest(ctx, f, &index, want, depth)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", tag, err)
			}
			if child == nil {
				return nil, fmt.Errorf("%s: index holds no manifest for %s/%s", tag, want.OS, want.Architecture)
			}
			a.platform = child.Platform
			desc = *child
			continue
		}

		child := selectPlatformManifest(&index)
		if child == nil {
			return nil, fmt.Errorf("%s: index holds no runnable platform manifest", tag)
		}
		a.platform = child.Platform
		desc = *child
	}
}

// findPlatformManifest searches an index's branches for the image
// manifest of one platform, descending through nested indexes — which
// state no platform of their own — and backtracking out of a branch
// that does not hold it.
func findPlatformManifest(ctx context.Context, f fetcher, index *ocispec.Index, want *ocispec.Platform, depth int) (*ocispec.Descriptor, error) {
	if depth > maxIndexDepth {
		return nil, fmt.Errorf("indexes nest deeper than any kit should")
	}
	var nested []ocispec.Descriptor
	for i := range index.Manifests {
		m := &index.Manifests[i]
		p := m.Platform
		switch {
		case p != nil && samePlatform(*p, *want):
			return m, nil
		case (p == nil || p.OS == "unknown") && isIndex(m.MediaType):
			nested = append(nested, *m)
		}
	}
	// Breadth first: a platform stated at this level is preferred to
	// one buried in a branch.
	for _, child := range nested {
		body, err := fetchAll(ctx, f, child)
		if err != nil {
			return nil, err
		}
		var inner ocispec.Index
		if err := json.Unmarshal(body, &inner); err != nil {
			return nil, fmt.Errorf("parse nested index: %w", err)
		}
		found, err := findPlatformManifest(ctx, f, &inner, want, depth+1)
		if err != nil {
			return nil, err
		}
		if found != nil {
			return &child, nil
		}
	}
	return nil, nil
}

// requirePlainImageManifest enforces the artifact shape the spec names: a
// plain image manifest — no artifactType — with an image-config blob.
// Anything else decodes as a manifest struct just fine, so without this
// gate an artifact of the wrong shape would sail through every check.
func requirePlainImageManifest(desc ocispec.Descriptor, m ocispec.Manifest) error {
	if m.SchemaVersion != 2 {
		return fmt.Errorf("a manifest declares schemaVersion 2, got %d", m.SchemaVersion)
	}
	mediaType := desc.MediaType
	if mediaType == "" {
		mediaType = m.MediaType
	}
	if mediaType != ocispec.MediaTypeImageManifest &&
		mediaType != "application/vnd.docker.distribution.manifest.v2+json" {
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

// ResolveKit reaches one of the kits a merged set lists, so its
// declarations can be compared against the merge that claims to carry
// them.
//
// A listed kit lives in a repository of its own — a different one, and
// possibly a different registry — so this opens a new client rather
// than reusing the fetcher that read this artifact. It is reached the
// way this artifact was: a set published to a loopback registry lists
// kits in that same registry, and holding them to HTTPS would make the
// check unrunnable exactly where a set is most often built.
//
// The reference is pinned to the digest the set recorded, which is what
// makes the comparison meaningful: a tag could have moved since, and
// then the check would judge the merge against something it never saw.
func (a *ociArtifact) ResolveKit(ctx context.Context, ref, digest string) (Artifact, error) {
	if a.reach == nil {
		return nil, errNoRegistry
	}
	// The digest names the target, so only the repository is wanted
	// here — parsed rather than cut, because a listed kit may carry a
	// tag, a digest, both, or neither, and a reference that failed to
	// split would skip the check instead of running it.
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, err
	}
	repo := reference.TrimNamed(named).String()

	// An explicit --plain-http answers for the registry the caller
	// named, not for every registry a set happens to list. Carrying it
	// further would send a request — and whatever credential the store
	// holds for that host — to a third party in the clear, on the
	// strength of a flag aimed somewhere else. A loopback registry
	// still needs no flag: remoteRepository recognizes it on its own.
	reach := registryOptions{plainHTTP: a.reach.plainHTTP && sameRegistry(named, a.origin)}
	r, err := remoteRepository(repo, reach)
	if err != nil {
		return nil, err
	}
	// For the platform being judged, not the host's. A merged set is
	// checked once per platform it publishes, and resolving a
	// multi-platform kit by the host architecture would compare one
	// platform's artifact against another's inputs.
	return resolveArtifactFor(ctx, r, digest, &reach, reference.Domain(named), a.judgedPlatform(ctx))
}

// samePlatform reports whether two descriptors name one platform.
//
// Normalized, not compared as written: BuildKit records a
// single-platform arm64 image with an empty variant while an index
// commonly spells the same platform arm64/v8, and amd64 has the same
// pair in v1. Refusing those as different would leave the declaration
// check skipping a set whose inputs are right there. Normalization
// collapses only the default spellings, so arm/v7 still does not
// answer for arm/v8.
func samePlatform(a, b ocispec.Platform) bool {
	a, b = platforms.Normalize(a), platforms.Normalize(b)
	return a.OS == b.OS && a.Architecture == b.Architecture && a.Variant == b.Variant
}

// judgedPlatform is the platform this artifact is being checked as.
//
// An artifact reached through an index carries the entry's platform.
// One reached directly carries none — a manifest does not state it —
// so the image config answers instead, which is where a single
// platform's image records what it was built for. Resolving a listed
// kit by anything else would compare an arm64 merged image against
// an amd64 input.
func (a *ociArtifact) judgedPlatform(ctx context.Context) *ocispec.Platform {
	if a.platform != nil {
		return a.platform
	}
	cfg, err := a.Config(ctx)
	if err != nil || cfg.OS == "" || cfg.Architecture == "" {
		return nil
	}
	return &ocispec.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
}

// sameRegistry reports whether a listed kit lives in the registry the
// caller named.
func sameRegistry(listed reference.Named, origin string) bool {
	return origin != "" && reference.Domain(listed) == origin
}

// errNoRegistry reports a source with no registry behind it — an OCI
// layout holds one artifact, and the kits a set lists are not in it.
var errNoRegistry = errors.New("this source cannot reach other kits")

// remoteRepository authenticates the way docker login does: the default
// client resolves no credentials at all, which would make every private
// kit uninspectable and report public ones only.
func remoteRepository(repo string, opts registryOptions) (*remote.Repository, error) {
	r, err := remote.NewRepository(repo)
	if err != nil {
		return nil, err
	}
	r.PlainHTTP = opts.plainHTTP || isLoopbackRegistry(r.Reference.Registry)
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("open docker credential store: %w", err)
	}
	r.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
	}
	return r, nil
}

// isLoopbackRegistry reports whether a registry host is this machine.
//
// A loopback registry serves plain HTTP without being asked, the same
// judgment docker and containerd make: the request never leaves the
// host, so there is no transit for a TLS-less exchange to be
// intercepted in, and a registry standing up for a build loop or a test
// has nothing to serve a certificate for. Every other host has to opt
// in through WithPlainHTTP, because there the absence of TLS is a
// property of a network someone else can be on.
func isLoopbackRegistry(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if name == "localhost" {
		return true
	}
	// A bracketed IPv6 literal survives SplitHostPort only when a port
	// was present; strip the brackets for the bare form.
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex ||
		mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

// selectPlatformManifest prefers this host's linux image and skips
// attestation manifests, which carry the unknown platform.
func selectPlatformManifest(index *ocispec.Index) *ocispec.Descriptor {
	var fallback *ocispec.Descriptor
	for i := range index.Manifests {
		m := &index.Manifests[i]
		p := m.Platform
		if p != nil && p.OS == "unknown" {
			continue
		}
		if p != nil && p.OS == "linux" && p.Architecture == runtime.GOARCH {
			return m
		}
		if fallback == nil {
			fallback = m
		}
	}
	return fallback
}

// maxMetadataBytes bounds an index, manifest, or config read. Metadata
// meets registry ceilings around 4 MB; a layout is untrusted input, and a
// descriptor pointing at an enormous blob must become an error, not an
// allocation.
const maxMetadataBytes = 8 << 20

func fetchAll(ctx context.Context, f fetcher, desc ocispec.Descriptor) ([]byte, error) {
	if desc.Size > maxMetadataBytes {
		return nil, fmt.Errorf("%s declares %d bytes; no kit metadata blob is that large", desc.Digest, desc.Size)
	}
	rc, err := f.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", desc.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", desc.Digest, err)
	}
	if len(body) > maxMetadataBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes; no kit metadata blob is that large", desc.Digest, maxMetadataBytes)
	}
	return body, nil
}

// ociArtifact answers the checks from a manifest and its blobs.
type ociArtifact struct {
	fetcher fetcher
	// reach is how this artifact's registry was reached, carried so
	// the kits a merged set lists — which live in repositories of
	// their own — can be opened. origin is the registry the caller
	// named, which is the only one an explicit transport choice
	// answers for.
	reach            *registryOptions
	origin           string
	manifest         ocispec.Manifest
	indexAnnotations map[string]string
	hasIndex         bool

	platform *ocispec.Platform
	config   *ocispec.Image
	// inventory caches every path the layers carry, since stem discovery
	// and each file read would otherwise walk them again.
	inventory []string
	// perLayerDirs caches one layer's directory paths: a directory
	// replacing a lower file hides it from filesystem resolution.
	perLayerDirs map[string][]string
	// perLayerLinks caches one layer's link entries: a symlink taking an
	// ancestor redirects every path beneath it.
	perLayerLinks map[string]map[string]assemble.LayerLink
	// perLayer caches one layer's paths, which whiteout resolution needs
	// layer by layer rather than flattened.
	perLayer map[string][]string
}

func (a *ociArtifact) Annotations() map[string]string { return a.manifest.Annotations }

func (a *ociArtifact) Layers(context.Context) ([]ocispec.Descriptor, bool, error) {
	return a.manifest.Layers, true, nil
}

func (a *ociArtifact) IndexAnnotations() (map[string]string, bool) {
	return a.indexAnnotations, a.hasIndex
}

func (a *ociArtifact) Config(ctx context.Context) (*ocispec.Image, error) {
	if a.config != nil {
		return a.config, nil
	}
	body, err := fetchAll(ctx, a.fetcher, a.manifest.Config)
	if err != nil {
		return nil, err
	}
	var img ocispec.Image
	if err := json.Unmarshal(body, &img); err != nil {
		return nil, fmt.Errorf("parse image config: %w", err)
	}
	a.config = &img
	return a.config, nil
}

// ReadFile resolves a path the way the composed filesystem does: later
// layers win, and a whiteout in a later layer deletes what earlier ones
// contributed. Ignoring whiteouts would let an artifact pass a staged-file
// check for a file that is not in the filesystem a runtime would see.
func (a *ociArtifact) ReadFile(ctx context.Context, name string) ([]byte, bool, error) {
	return a.readFileAt(ctx, name, 0, len(a.manifest.Layers)-1)
}

// HasFile reports presence without fetching content, so existence checks
// impose no size bound on bodies that are deliberately bulky, such as a
// staged context.
func (a *ociArtifact) HasFile(ctx context.Context, name string) (bool, error) {
	return a.hasFileAt(ctx, name, 0, len(a.manifest.Layers)-1)
}

// maxLinkDepth bounds link chasing: layers are untrusted input, and a
// link cycle would otherwise resolve forever.
const maxLinkDepth = 8

// winningLayer names the layer within layers[:upto+1] whose entry the
// composed filesystem exposes for name, or -1. Only the cached
// inventories are consulted; nothing is fetched.
func (a *ociArtifact) winningLayer(ctx context.Context, name string, upto int) (int, error) {
	target := strings.TrimPrefix(name, "/")
	winner := -1
	for i := 0; i <= upto && i < len(a.manifest.Layers); i++ {
		paths, dirs, _, err := a.layerEntries(ctx, a.manifest.Layers[i])
		if err != nil {
			return -1, err
		}
		if whitesOut(paths, name) {
			winner = -1
		}
		// A directory taking the path replaces whatever file a lower
		// layer put there; as a file, the path is gone.
		for _, d := range dirs {
			if d == target {
				winner = -1
				break
			}
		}
		for _, p := range paths {
			if p == target {
				winner = i
				break
			}
		}
	}
	return winner, nil
}

// pathKind is what the composed filesystem holds at one path.
type pathKind int

const (
	kindAbsent pathKind = iota
	kindDir
	kindFile
	kindSymlink
)

// composedKind reports what a path finally is across layers[:upto+1],
// with the symlink's target when it is one.
func (a *ociArtifact) composedKind(ctx context.Context, name string, upto int) (pathKind, string, error) {
	target := strings.TrimPrefix(name, "/")
	kind, linkTarget := kindAbsent, ""
	for i := 0; i <= upto && i < len(a.manifest.Layers); i++ {
		paths, dirs, links, err := a.layerEntries(ctx, a.manifest.Layers[i])
		if err != nil {
			return kindAbsent, "", err
		}
		if whitesOut(paths, name) {
			kind, linkTarget = kindAbsent, ""
		}
		for _, d := range dirs {
			if d == target {
				kind, linkTarget = kindDir, ""
				break
			}
		}
		for _, p := range paths {
			if p == target {
				kind, linkTarget = kindFile, ""
				break
			}
		}
		if l, ok := links[target]; ok && !l.Hard {
			kind, linkTarget = kindSymlink, l.Target
		}
	}
	return kind, linkTarget, nil
}

// resolveAncestors rewrites name through any symlinked ancestor and
// reports whether a non-directory ancestor hides the subtree instead: the
// composed filesystem cannot expose a path through what is not a
// directory, but it happily exposes one through a symlink to one.
func (a *ociArtifact) resolveAncestors(ctx context.Context, name string, depth, upto int) (string, bool, error) {
	if depth > maxLinkDepth {
		return "", false, fmt.Errorf("%s: links nest deeper than any kit should", name)
	}
	target := strings.TrimPrefix(name, "/")
	segments := strings.Split(target, "/")
	for i := 1; i < len(segments); i++ {
		ancestor := strings.Join(segments[:i], "/")
		kind, linkTarget, err := a.composedKind(ctx, ancestor, upto)
		if err != nil {
			return "", false, err
		}
		switch kind {
		case kindSymlink:
			// An empty target is dangling: nothing beneath it is
			// reachable, and following it as if it named the parent
			// would resurrect the subtree.
			if linkTarget == "" {
				return "", true, nil
			}
			rest := strings.Join(segments[i:], "/")
			// Joined canonically: a target of "/" concatenated naively
			// yields "//<rest>", which no archive path matches.
			base := resolveLinkTarget("/"+ancestor, assemble.FileEntry{Link: linkTarget})
			return a.resolveAncestors(ctx, path.Join(base, rest), depth+1, upto)
		case kindFile:
			return "", true, nil
		}
	}
	return "/" + target, false, nil
}

// resolveLinkTarget names the path a link entry at name points to. A hard
// link names its target from the archive root; a symlink resolves against
// its own directory.
func resolveLinkTarget(name string, entry assemble.FileEntry) string {
	// Every branch cleans: a valid absolute target like /opt/../staged
	// names /staged in the image, and searching for the literal dotted
	// archive path would report a present file missing.
	switch {
	case entry.Hard:
		return path.Clean("/" + strings.TrimPrefix(entry.Link, "/"))
	case !strings.HasPrefix(entry.Link, "/"):
		return path.Join(path.Dir("/"+strings.TrimPrefix(name, "/")), entry.Link)
	default:
		return path.Clean(entry.Link)
	}
}

func (a *ociArtifact) readFileAt(ctx context.Context, name string, depth, upto int) ([]byte, bool, error) {
	if depth > maxLinkDepth {
		return nil, false, fmt.Errorf("%s: links nest deeper than any kit should", name)
	}
	name, hidden, err := a.resolveAncestors(ctx, name, depth, upto)
	if err != nil || hidden {
		return nil, false, err
	}
	winner, err := a.winningLayer(ctx, name, upto)
	if err != nil || winner < 0 {
		return nil, false, err
	}

	layer := a.manifest.Layers[winner]
	rc, err := a.fetcher.Fetch(ctx, layer)
	if err != nil {
		return nil, false, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	entry, err := assemble.ReadFileEntry(rc, name)
	if err != nil {
		return nil, false, err
	}
	if entry.Linked {
		if entry.Link == "" {
			// Dangling by construction: no target exists to serve.
			return nil, false, nil
		}
		if entry.Hard {
			return a.resolveHardLink(ctx, winner, upto,
				"/"+strings.TrimPrefix(entry.Link, "/"), entry.Index, depth+1)
		}
		return a.readFileAt(ctx, resolveLinkTarget(name, entry), depth+1, upto)
	}
	return entry.Body, entry.OK, nil
}

// resolveHardLink serves what a hard link materialized: its target's
// inode at the moment the link entry applied. Within the link's own
// layer, that is the target's last entry BEFORE the link — a later
// rewrite in the same tar replaces the path with a new inode and must not
// retarget the link; below it, the composed state of the lower layers.
func (a *ociArtifact) resolveHardLink(ctx context.Context, winner, upto int, target string, before, depth int) ([]byte, bool, error) {
	if depth > maxLinkDepth {
		return nil, false, fmt.Errorf("%s: links nest deeper than any kit should", target)
	}
	layer := a.manifest.Layers[winner]
	rc, err := a.fetcher.Fetch(ctx, layer)
	if err != nil {
		return nil, false, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	prior, err := assemble.ReadFileEntryBefore(rc, target, before)
	if err != nil {
		return nil, false, err
	}
	switch {
	case prior.OK && !prior.Linked:
		return prior.Body, true, nil
	case prior.OK && prior.Link == "":
		return nil, false, nil
	case prior.OK && prior.Hard:
		return a.resolveHardLink(ctx, winner, upto,
			"/"+strings.TrimPrefix(prior.Link, "/"), prior.Index, depth+1)
	case prior.OK:
		// A symlink resolves at read time, against the final state.
		return a.readFileAt(ctx, resolveLinkTarget(target, prior), depth+1, upto)
	default:
		// Nothing earlier in this layer: the target came from below.
		return a.readFileAt(ctx, target, depth+1, winner-1)
	}
}

func (a *ociArtifact) hasFileAt(ctx context.Context, name string, depth, upto int) (bool, error) {
	if depth > maxLinkDepth {
		return false, fmt.Errorf("%s: links nest deeper than any kit should", name)
	}
	name, hidden, err := a.resolveAncestors(ctx, name, depth, upto)
	if err != nil || hidden {
		return false, err
	}
	winner, err := a.winningLayer(ctx, name, upto)
	if err != nil || winner < 0 {
		return false, err
	}
	layer := a.manifest.Layers[winner]
	rc, err := a.fetcher.Fetch(ctx, layer)
	if err != nil {
		return false, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	entry, err := assemble.StatFileEntry(rc, name)
	if err != nil {
		return false, err
	}
	if entry.Linked {
		// A dangling link is not a present file, and an empty target is
		// dangling by construction.
		if entry.Link == "" {
			return false, nil
		}
		if entry.Hard {
			return a.hasHardLink(ctx, winner, upto,
				"/"+strings.TrimPrefix(entry.Link, "/"), entry.Index, depth+1)
		}
		return a.hasFileAt(ctx, resolveLinkTarget(name, entry), depth+1, upto)
	}
	return entry.OK, nil
}

// hasHardLink is resolveHardLink for existence only: no target body is
// ever buffered, so no content bound applies to bulky staged files
// reached through a link.
func (a *ociArtifact) hasHardLink(ctx context.Context, winner, upto int, target string, before, depth int) (bool, error) {
	if depth > maxLinkDepth {
		return false, fmt.Errorf("%s: links nest deeper than any kit should", target)
	}
	layer := a.manifest.Layers[winner]
	rc, err := a.fetcher.Fetch(ctx, layer)
	if err != nil {
		return false, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	prior, err := assemble.StatFileEntryBefore(rc, target, before)
	if err != nil {
		return false, err
	}
	switch {
	case prior.OK && !prior.Linked:
		return true, nil
	case prior.OK && prior.Link == "":
		return false, nil
	case prior.OK && prior.Hard:
		return a.hasHardLink(ctx, winner, upto,
			"/"+strings.TrimPrefix(prior.Link, "/"), prior.Index, depth+1)
	case prior.OK:
		return a.hasFileAt(ctx, resolveLinkTarget(target, prior), depth+1, upto)
	default:
		return a.hasFileAt(ctx, target, depth+1, winner-1)
	}
}

// layerPaths lists one layer's entries, cached because resolution walks
// every layer for every path.
func (a *ociArtifact) layerEntries(ctx context.Context, layer ocispec.Descriptor) (files, dirs []string, links map[string]assemble.LayerLink, err error) {
	if a.perLayer == nil {
		a.perLayer = map[string][]string{}
		a.perLayerDirs = map[string][]string{}
		a.perLayerLinks = map[string]map[string]assemble.LayerLink{}
	}
	key := layer.Digest.String()
	if paths, ok := a.perLayer[key]; ok {
		return paths, a.perLayerDirs[key], a.perLayerLinks[key], nil
	}
	rc, err := a.fetcher.Fetch(ctx, layer)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
	}
	files, dirs, links, err = assemble.ReadLayerEntries(rc)
	_ = rc.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	a.perLayer[key] = files
	a.perLayerDirs[key] = dirs
	a.perLayerLinks[key] = links
	return files, dirs, links, nil
}

// whitesOut reports whether a layer deletes name: directly, by deleting
// any ancestor directory, or by marking one opaque. Deleting a parent
// removes everything beneath it, so checking only the file itself would
// still find sources the composed filesystem does not have.
func whitesOut(paths []string, name string) bool {
	target := strings.TrimPrefix(name, "/")

	deletions := map[string]bool{}
	for _, p := range paths {
		if base := path.Base(p); strings.HasPrefix(base, ".wh.") && base != ".wh..wh..opq" {
			deletions[path.Join(path.Dir(p), strings.TrimPrefix(base, ".wh."))] = true
		}
		// An opaque directory hides everything the lower layers put in
		// it, and a root-level marker hides everything there is.
		if path.Base(p) == ".wh..wh..opq" {
			if dir := path.Dir(p); dir == "." || strings.HasPrefix(target, dir+"/") {
				return true
			}
		}
	}
	for ancestor := target; ancestor != "." && ancestor != "/"; ancestor = path.Dir(ancestor) {
		if deletions[ancestor] {
			return true
		}
	}
	return false
}

func (a *ociArtifact) StagedStems(ctx context.Context) ([]string, error) {
	if a.inventory == nil {
		// Candidates are every descriptor-shaped path any layer ever
		// carried; whether each survives composition is answered by the
		// SAME resolution content reads use, so the two can never
		// disagree about what the filesystem exposes — whiteouts,
		// directory replacement, and ancestor replacement included.
		last := len(a.manifest.Layers) - 1
		// The staged root itself may sit behind symlinked ancestors, so
		// stems are discovered where the composed filesystem really keeps
		// them, while the inventory records the canonical consumer-facing
		// paths, alive-tested through the same resolution reads use.
		resolvedRoot, hidden, err := a.resolveAncestors(ctx,
			StagedKitRoot+"/"+stagedDescriptorName, 0, last)
		if err != nil {
			return nil, err
		}
		effectiveRoot := StagedKitRoot
		if !hidden {
			effectiveRoot = path.Dir(resolvedRoot)
		}
		kind, linkTarget, err := a.composedKind(ctx, effectiveRoot, last)
		if err != nil {
			return nil, err
		}
		if kind == kindSymlink {
			if linkTarget == "" {
				// A dangling root stages nothing.
				return stemsFromPaths(nil), nil
			}
			effectiveRoot = resolveLinkTarget(effectiveRoot, assemble.FileEntry{Link: linkTarget})
		}
		prefix := strings.TrimPrefix(effectiveRoot, "/") + "/"
		stems := map[string]bool{}
		for _, layer := range a.manifest.Layers {
			paths, _, links, err := a.layerEntries(ctx, layer)
			if err != nil {
				return nil, err
			}
			for _, p := range paths {
				rest, ok := strings.CutPrefix(p, prefix)
				if !ok {
					continue
				}
				if stem, file := path.Split(rest); file == stagedDescriptorName &&
					stem != "" && !strings.Contains(strings.TrimSuffix(stem, "/"), "/") {
					stems[strings.TrimSuffix(stem, "/")] = true
				}
				// A symlinked stem directory stages its descriptor
				// elsewhere; the stem is still the directory's name.
				if l, ok := links[p]; ok && !l.Hard && rest != "" && !strings.Contains(rest, "/") {
					stems[rest] = true
				}
			}
		}
		a.inventory = []string{}
		canonical := strings.TrimPrefix(StagedKitRoot, "/") + "/"
		for stem := range stems {
			candidate := canonical + stem + "/" + stagedDescriptorName
			alive, err := a.hasFileAt(ctx, "/"+candidate, 0, last)
			if err != nil {
				return nil, err
			}
			if alive {
				a.inventory = append(a.inventory, candidate)
			}
		}
		sort.Strings(a.inventory)
	}
	return stemsFromPaths(a.inventory), nil
}

// stemsFromPaths reports the kit roots staged under StagedKitRoot.
func stemsFromPaths(paths []string) []string {
	prefix := strings.TrimPrefix(StagedKitRoot, "/") + "/"
	seen := map[string]bool{}
	var stems []string
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		stem, file := path.Split(rest)
		stem = strings.TrimSuffix(stem, "/")
		// Exactly one segment: the layout is <root>/<stem>/kit.yaml, so a
		// descriptor buried deeper is an unrelated file, not a staged kit.
		if stem == "" || strings.Contains(stem, "/") || file != stagedDescriptorName || seen[stem] {
			continue
		}
		seen[stem] = true
		stems = append(stems, stem)
	}
	return stems
}

// dirFS adapts a directory to the fs.FS an OCI layout store reads.
func dirFS(dir string) fs.FS { return os.DirFS(dir) }

// Platform names which image an artifact is, so a report over several says
// which one it is talking about.
func (a *ociArtifact) Platform() string {
	if a.platform == nil {
		return ""
	}
	label := a.platform.OS + "/" + a.platform.Architecture
	// Without the variant, linux/arm/v6 and linux/arm/v7 collapse into one
	// heading and a failing report no longer says which manifest it judged.
	if a.platform.Variant != "" {
		label += "/" + a.platform.Variant
	}
	return label
}
