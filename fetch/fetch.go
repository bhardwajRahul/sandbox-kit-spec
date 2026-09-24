package fetch

import (
	"context"
	"fmt"
	"net/http"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// ErrNotAKit is returned when a reference resolves to an image whose
// manifest carries no kit descriptor annotation.
var ErrNotAKit = fmt.Errorf("manifest carries no %s annotation", spec.AnnotationDescriptor)

// Kit is one published kit as the registry served it.
type Kit struct {
	// Reference is the reference the caller named.
	Reference string

	// Digest is the digest the reference resolved to. A multi-platform
	// kit pins the index; a single-platform kit pins its manifest.
	Digest string

	// Descriptor is the published descriptor.
	Descriptor *spec.Descriptor

	// Raw is the annotation bytes Descriptor was decoded from, which
	// ValidatePublished needs alongside the struct.
	Raw []byte
}

// Option configures a Client. Later options win.
type Option func(*config) error

type config struct {
	credential auth.CredentialFunc
	transport  http.RoundTripper
	plainHTTP  bool
	platform   ocispec.Platform
}

// WithCredential supplies registry credentials. Without it, requests are
// anonymous, which is enough for a public registry.
//
// The function receives the registry host (host:port) the request is
// about to authenticate to. Return auth.EmptyCredential for a host that
// should stay anonymous.
func WithCredential(fn auth.CredentialFunc) Option {
	return func(c *config) error {
		c.credential = fn
		return nil
	}
}

// DockerCredential reads the credential store `docker login` writes.
// Compose it inside WithCredential when only some registries should
// come from that store.
func DockerCredential() (auth.CredentialFunc, error) {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("open docker credential store: %w", err)
	}
	return credentials.Credential(store), nil
}

// WithDockerCredentials authenticates with DockerCredential.
func WithDockerCredentials() Option {
	return func(c *config) error {
		fn, err := DockerCredential()
		if err != nil {
			return err
		}
		c.credential = fn
		return nil
	}
}

// WithTransport sets the HTTP transport under the registry client.
// The default retry policy still wraps it. Nil uses http.DefaultTransport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) error {
		c.transport = rt
		return nil
	}
}

// WithPlainHTTP reaches every registry over HTTP.
//
// A loopback registry does this without the option: the request never
// leaves the machine. Anywhere else, plain HTTP is a choice about a
// network someone else can be on, so it has to be asked for.
func WithPlainHTTP() Option {
	return func(c *config) error {
		c.plainHTTP = true
		return nil
	}
}

// WithPlatform selects which platform manifest to read when an index
// carries no descriptor annotation. The default is linux on this
// process's architecture: a kit is a Linux image whichever OS the
// caller is running.
func WithPlatform(p ocispec.Platform) Option {
	return func(c *config) error {
		c.platform = p
		return nil
	}
}

// Client fetches kit descriptors. It is safe for concurrent use.
type Client struct {
	credential auth.CredentialFunc
	transport  http.RoundTripper
	plainHTTP  bool
	platform   ocispec.Platform
	cache      auth.Cache
}

// New returns a client. With no options it pulls public kits anonymously,
// over HTTPS except for a loopback registry.
func New(opts ...Option) (*Client, error) {
	cfg := config{platform: defaultPlatform()}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}
	return &Client{
		credential: cfg.credential,
		transport:  cfg.transport,
		plainHTTP:  cfg.plainHTTP,
		platform:   cfg.platform,
		cache:      auth.NewCache(),
	}, nil
}

// Fetch reads one kit's published descriptor. It fetches manifests only.
func (c *Client) Fetch(ctx context.Context, ref string) (*Kit, error) {
	k, err := c.fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ref, err)
	}
	return k, nil
}

// Request is one kit to assemble. Args are that kit's create-phase
// values, keyed by its own arg names, so two kits that declare the
// same name still receive their own values.
type Request struct {
	Reference string
	Args      map[string]string
}

// Assemble fetches the requests, resolves them as a runnable set —
// exactly one workload — and merges their descriptors.
//
// Each kit's create-phase args are resolved and expanded before the
// merge, which is what spec.Merge requires of a contribution. A
// required arg with no value is an error, and a value that is still a
// ${{ kit.args.* }} reference is refused: an assembled spec has no
// set declaration left to bound a re-export. Build-phase args are
// already baked into the annotation.
//
// Contributions are ordered by the dependency graph, providers before
// the kits that require them, which is the order declaration merge
// means by "first".
func (c *Client) Assemble(ctx context.Context, reqs []Request, opts spec.MergeOptions) (*spec.MergeResult, error) {
	return c.assemble(ctx, reqs, opts, false)
}

// AssemblePartial is Assemble for a set that does not have to be
// runnable. A set of mixins merges to a mixin and composes onto a
// workload later. More than one workload is still an error.
func (c *Client) AssemblePartial(ctx context.Context, reqs []Request, opts spec.MergeOptions) (*spec.MergeResult, error) {
	return c.assemble(ctx, reqs, opts, true)
}

func (c *Client) assemble(ctx context.Context, reqs []Request, opts spec.MergeOptions, partial bool) (*spec.MergeResult, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("fetch: empty kit set")
	}
	kits := make([]*Kit, 0, len(reqs))
	args := make([]map[string]string, 0, len(reqs))
	for _, req := range reqs {
		k, err := c.Fetch(ctx, req.Reference)
		if err != nil {
			return nil, err
		}
		kits = append(kits, k)
		args = append(args, req.Args)
	}
	return mergeKits(kits, args, opts, partial)
}

// Units builds the resolver's input from fetched kits.
//
// Each descriptor is copied and its version set to the one the
// reference's tag supplies, so a merge records the same version the
// resolver judged. The fetched Kit is left as the registry served it.
func Units(kits []*Kit) ([]*resolve.Unit, error) {
	units := make([]*resolve.Unit, 0, len(kits))
	for _, k := range kits {
		if k == nil || k.Descriptor == nil {
			return nil, fmt.Errorf("fetch: kit %s has no descriptor", refOf(k))
		}
		d := *k.Descriptor
		d.Version = resolve.EffectiveProvideVersion(k.Reference, k.Descriptor)
		image, err := pinnedImage(k.Reference, k.Digest)
		if err != nil {
			return nil, fmt.Errorf("fetch: kit %s: %w", k.Reference, err)
		}
		units = append(units, &resolve.Unit{
			Reference:  k.Reference,
			Digest:     k.Digest,
			Image:      image,
			Descriptor: &d,
		})
	}
	return units, nil
}

func mergeKits(kits []*Kit, args []map[string]string, opts spec.MergeOptions, partial bool) (*spec.MergeResult, error) {
	prepared := make([]*Kit, len(kits))
	for i, k := range kits {
		d, raw, err := expandKit(k, args[i])
		if err != nil {
			return nil, err
		}
		cp := *k
		cp.Descriptor = d
		cp.Raw = raw
		prepared[i] = &cp
	}
	units, err := Units(prepared)
	if err != nil {
		return nil, err
	}
	var resolution *resolve.Resolution
	if partial {
		resolution, err = resolve.ResolvePartial(units)
	} else {
		resolution, err = resolve.Resolve(units)
	}
	if err != nil {
		return nil, err
	}

	contributions := make([]spec.Contribution, 0, len(units))
	for _, u := range resolution.Topological() {
		contributions = append(contributions, spec.Contribution{
			Reference:  u.Reference,
			Descriptor: u.Descriptor,
		})
	}
	return spec.Merge(contributions, opts)
}

// expandKit resolves one kit's create-phase args into its published
// descriptor. The fetched Kit is left as the registry served it.
func expandKit(k *Kit, args map[string]string) (*spec.Descriptor, []byte, error) {
	if k == nil || k.Descriptor == nil {
		return nil, nil, fmt.Errorf("fetch: kit %s has no descriptor", refOf(k))
	}
	if _, err := spec.ValidatePublished(k.Raw, k.Descriptor); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	values, err := spec.KitArgValues(k.Descriptor.Args, args)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	for name, value := range values {
		if spec.ContainsArgRef(value) {
			return nil, nil, fmt.Errorf("%s: arg %q must be a literal value, got %q", k.Reference, name, value)
		}
	}
	expanded, err := spec.ExpandCreateArgs(k.Raw, k.Descriptor.Args, values)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	d, err := spec.Decode(expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	if _, err := spec.ValidateEffective(expanded, d); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	d.Args = nil
	return d, expanded, nil
}

func refOf(k *Kit) string {
	if k == nil || k.Reference == "" {
		return "(none)"
	}
	return k.Reference
}

func pinnedImage(ref, dgst string) (string, error) {
	named, err := parseNamed(ref)
	if err != nil {
		return "", err
	}
	parsed, err := digest.Parse(dgst)
	if err != nil {
		return "", fmt.Errorf("digest %q: %w", dgst, err)
	}
	canonical, err := withDigest(named, parsed)
	if err != nil {
		return "", err
	}
	return canonical, nil
}
