package fetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestAssembleReadsDescriptorsAndMergesInGraphOrder(t *testing.T) {
	reg := newRegistry(t)
	tool := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"tool@1.0.0"},
	}))
	hello := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindWorkload,
		Version:       "1.0.0",
		Provides:      []string{"hello@1.0.0"},
		Requires:      []string{"tool"},
	}))
	reg.tag("kits/tool", "1.0.0", tool)
	reg.tag("kits/hello", "1.0.0", hello)

	client, err := New()
	require.NoError(t, err)
	merged, err := client.Assemble(context.Background(), reqs(
		reg.ref("kits/hello", "1.0.0"),
		reg.ref("kits/tool", "1.0.0"),
	), spec.MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, spec.KindWorkload, merged.Descriptor.Kind)
	require.Equal(t, []string{"tool@1.0.0", "hello@1.0.0"}, merged.Descriptor.Provides)
	require.Empty(t, merged.Descriptor.Requires)
	require.Zero(t, reg.blobReads, "assembling a spec fetches manifests, not layers")
}

func TestAVersionShapedTagOverridesAStaleDescriptor(t *testing.T) {
	reg := newRegistry(t)
	hello := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindWorkload,
		Version:       "1.0.0",
		Provides:      []string{"hello"},
	}))
	reg.tag("kits/hello", "2.0.0", hello)

	client, err := New()
	require.NoError(t, err)
	merged, err := client.Assemble(context.Background(), reqs(reg.ref("kits/hello", "2.0.0")), spec.MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"hello@2.0.0"}, merged.Descriptor.Provides)
}

func TestAssemblePartialAllowsASetOfMixins(t *testing.T) {
	reg := newRegistry(t)
	tool := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"tool@1.0.0"},
	}))
	reg.tag("kits/tool", "1.0.0", tool)
	refs := reqs(reg.ref("kits/tool", "1.0.0"))

	client, err := New()
	require.NoError(t, err)
	_, err = client.Assemble(context.Background(), refs, spec.MergeOptions{})
	require.ErrorContains(t, err, "no workload kit")

	merged, err := client.AssemblePartial(context.Background(), refs, spec.MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, spec.KindMixin, merged.Descriptor.Kind)
}

func TestCreatePhaseArgsAreResolvedPerKitBeforeMerge(t *testing.T) {
	reg := newRegistry(t)
	parameterized := func(name string) []byte {
		return kitJSON(t, &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion,
			Kind:          spec.KindMixin,
			Version:       "1.0.0",
			Provides:      []string{name + "@1.0.0"},
			Args: map[string]spec.Arg{
				"host": {Required: true},
			},
			Capabilities: []spec.Capability{{
				Type: spec.CapabilityNetworkPolicy,
				Config: map[string]any{
					"runtime": map[string]any{
						"allow": []any{"${{ kit.args.host }}"},
					},
				},
			}},
		})
	}
	reg.tag("kits/a", "1.0.0", reg.image(t, parameterized("a")))
	reg.tag("kits/b", "1.0.0", reg.image(t, parameterized("b")))

	client, err := New()
	require.NoError(t, err)
	refA, refB := reg.ref("kits/a", "1.0.0"), reg.ref("kits/b", "1.0.0")
	_, err = client.AssemblePartial(context.Background(), []Request{{Reference: refA}}, spec.MergeOptions{})
	require.ErrorContains(t, err, "required")

	merged, err := client.AssemblePartial(context.Background(), []Request{
		{Reference: refA, Args: map[string]string{"host": "a.example"}},
		{Reference: refB, Args: map[string]string{"host": "b.example"}},
	}, spec.MergeOptions{})
	require.NoError(t, err)
	policy, err := spec.NetworkPolicyOf(merged.Descriptor.Capabilities)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.example", "b.example"}, policy.Runtime.Allow)
	require.Empty(t, merged.Descriptor.Args)
}

func TestIndexAnnotationIsEnough(t *testing.T) {
	reg := newRegistry(t)
	// The platform manifest is deliberately not stored. Reading it
	// would 404, so success means the index annotation was enough.
	desc := kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	})
	missing := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromString("absent"),
		Size:      1,
		Platform:  &ocispec.Platform{OS: "linux", Architecture: "amd64"},
	}
	reg.tag("kits/demo", "1.0.0", reg.index(t, []ocispec.Descriptor{missing}, map[string]string{
		spec.AnnotationDescriptor: string(desc),
	}))

	client, err := New()
	require.NoError(t, err)
	got, err := client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.NoError(t, err)
	require.Equal(t, []string{"demo@1.0.0"}, got.Descriptor.Provides)
	require.Equal(t, digest.FromBytes(reg.tagged["kits/demo:1.0.0"]).String(), got.Digest)
}

func TestAMissingIndexAnnotationFallsBackToThePlatformManifest(t *testing.T) {
	reg := newRegistry(t)
	amd64 := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		DisplayName:   "amd64",
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	arm64 := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		DisplayName:   "arm64",
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	reg.tag("kits/demo", "1.0.0", reg.index(t, []ocispec.Descriptor{
		reg.platform("kits/demo", amd64, "amd64"),
		reg.platform("kits/demo", arm64, "arm64"),
	}, nil))

	client, err := New(WithPlatform(ocispec.Platform{OS: "linux", Architecture: "arm64"}))
	require.NoError(t, err)
	got, err := client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.NoError(t, err)
	require.Equal(t, "arm64", got.Descriptor.DisplayName)
	require.Equal(t, digest.FromBytes(reg.tagged["kits/demo:1.0.0"]).String(), got.Digest,
		"the pin is the index the tag resolved to, not the platform manifest")
}

func TestACloserPlatformBeatsACompatibleOneLaterInTheIndex(t *testing.T) {
	reg := newRegistry(t)
	amd64 := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		DisplayName:   "amd64",
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	i386 := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		DisplayName:   "386",
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	// amd64 is listed first and 386 second. platforms.Only treats 386 as
	// compatible with amd64, so keeping the last match would return 386.
	reg.tag("kits/demo", "1.0.0", reg.index(t, []ocispec.Descriptor{
		reg.platform("kits/demo", amd64, "amd64"),
		reg.platform("kits/demo", i386, "386"),
	}, nil))

	client, err := New(WithPlatform(ocispec.Platform{OS: "linux", Architecture: "amd64"}))
	require.NoError(t, err)
	got, err := client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.NoError(t, err)
	require.Equal(t, "amd64", got.Descriptor.DisplayName)
}

func TestAnIndexWithoutThePlatformIsAnError(t *testing.T) {
	reg := newRegistry(t)
	arm64 := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	reg.tag("kits/demo", "1.0.0", reg.index(t, []ocispec.Descriptor{
		reg.platform("kits/demo", arm64, "arm64"),
	}, nil))

	client, err := New(WithPlatform(ocispec.Platform{OS: "linux", Architecture: "amd64"}))
	require.NoError(t, err)
	_, err = client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.ErrorContains(t, err, "linux/amd64")
}

func TestAnImageWithoutTheAnnotationIsNotAKit(t *testing.T) {
	reg := newRegistry(t)
	reg.tag("kits/plain", "1.0.0", reg.image(t, nil))

	client, err := New()
	require.NoError(t, err)
	_, err = client.Fetch(context.Background(), reg.ref("kits/plain", "1.0.0"))
	require.ErrorIs(t, err, ErrNotAKit)
}

func TestBasicAuthUsesTheCallerCredential(t *testing.T) {
	reg := newRegistry(t)
	reg.user, reg.pass = "me", "secret"
	demo := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	reg.tag("kits/demo", "1.0.0", demo)

	client, err := New(WithCredential(func(context.Context, string) (auth.Credential, error) {
		return auth.Credential{Username: "me", Password: "secret"}, nil
	}))
	require.NoError(t, err)
	_, err = client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.NoError(t, err)
	require.NotZero(t, reg.challenges, "a credential is sent because the registry asked, not ahead of the request")

	denied, err := New(WithCredential(func(context.Context, string) (auth.Credential, error) {
		return auth.Credential{Username: "me", Password: "nope"}, nil
	}))
	require.NoError(t, err)
	_, err = denied.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.Error(t, err)
}

func TestTransportIsTheCallers(t *testing.T) {
	reg := newRegistry(t)
	demo := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	reg.tag("kits/demo", "1.0.0", demo)

	var rt markingTransport
	rt.base = http.DefaultTransport
	client, err := New(WithTransport(&rt))
	require.NoError(t, err)
	_, err = client.Fetch(context.Background(), reg.ref("kits/demo", "1.0.0"))
	require.NoError(t, err)
	require.NotZero(t, rt.hits)
}

func TestPlainHTTPIsImpliedForLoopbackOnly(t *testing.T) {
	client, err := New()
	require.NoError(t, err)
	repo, err := client.repository("docker.io/me/kit")
	require.NoError(t, err)
	require.False(t, repo.PlainHTTP)

	repo, err = client.repository("127.0.0.1:5000/kit")
	require.NoError(t, err)
	require.True(t, repo.PlainHTTP)

	client, err = New(WithPlainHTTP())
	require.NoError(t, err)
	repo, err = client.repository("docker.io/me/kit")
	require.NoError(t, err)
	require.True(t, repo.PlainHTTP)
}

func TestFetchedDigestPinsTheImageReference(t *testing.T) {
	reg := newRegistry(t)
	demo := reg.image(t, kitJSON(t, &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
	}))
	reg.tag("kits/demo", "1.0.0", demo)

	client, err := New()
	require.NoError(t, err)
	ref := reg.ref("kits/demo", "1.0.0")
	got, err := client.Fetch(context.Background(), ref)
	require.NoError(t, err)

	units, err := Units([]*Kit{got})
	require.NoError(t, err)
	require.Equal(t, got.Digest, units[0].Digest)
	require.True(t, strings.HasSuffix(units[0].Image, "@"+got.Digest))
	require.Equal(t, "1.0.0", units[0].Descriptor.Version)
	require.Equal(t, "1.0.0", got.Descriptor.Version, "fetch leaves the published descriptor alone")
}

type markingTransport struct {
	base http.RoundTripper
	hits int
	mu   sync.Mutex
}

func (m *markingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.hits++
	m.mu.Unlock()
	return m.base.RoundTrip(req)
}

func reqs(refs ...string) []Request {
	out := make([]Request, len(refs))
	for i, ref := range refs {
		out[i] = Request{Reference: ref}
	}
	return out
}

func kitJSON(t *testing.T, d *spec.Descriptor) []byte {
	t.Helper()
	if d == nil {
		return nil
	}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	return raw
}

// registry is a distribution server that stores manifests and refuses
// blob reads, so a test can see that a descriptor fetch stayed in the
// manifest.
type registry struct {
	*httptest.Server
	mu         sync.Mutex
	manifests  map[string][]byte
	mediaType  map[string]string
	tagged     map[string][]byte
	user       string
	pass       string
	blobReads  int
	challenges int
}

func newRegistry(t *testing.T) *registry {
	t.Helper()
	r := &registry{
		manifests: map[string][]byte{},
		mediaType: map[string]string{},
		tagged:    map[string][]byte{},
	}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *registry) ref(repo, tag string) string {
	return strings.TrimPrefix(r.URL, "http://") + "/" + repo + ":" + tag
}

func (r *registry) image(t *testing.T, descriptor []byte) []byte {
	t.Helper()
	m := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.FromString("config"),
			Size:      2,
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    digest.FromString("layer"),
			Size:      1,
		}},
	}
	if descriptor != nil {
		m.Annotations = map[string]string{spec.AnnotationDescriptor: string(descriptor)}
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return raw
}

func (r *registry) index(t *testing.T, manifests []ocispec.Descriptor, annotations map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(ocispec.Index{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		MediaType:   ocispec.MediaTypeImageIndex,
		Manifests:   manifests,
		Annotations: annotations,
	})
	require.NoError(t, err)
	return raw
}

func (r *registry) platform(repo string, manifest []byte, arch string) ocispec.Descriptor {
	dgst := digest.FromBytes(manifest)
	key := repo + "/manifests/" + dgst.String()
	r.mu.Lock()
	r.manifests[key] = manifest
	r.mediaType[key] = ocispec.MediaTypeImageManifest
	r.mu.Unlock()
	plat := ocispec.Platform{OS: "linux", Architecture: arch}
	return ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    dgst,
		Size:      int64(len(manifest)),
		Platform:  &plat,
	}
}

func (r *registry) tag(repo, tag string, manifest []byte) {
	mediaType := ocispec.MediaTypeImageManifest
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	_ = json.Unmarshal(manifest, &probe)
	if probe.MediaType != "" {
		mediaType = probe.MediaType
	}
	key := repo + ":" + tag
	dgst := digest.FromBytes(manifest).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tagged[key] = manifest
	r.manifests[repo+"/manifests/"+tag] = manifest
	r.mediaType[repo+"/manifests/"+tag] = mediaType
	r.manifests[repo+"/manifests/"+dgst] = manifest
	r.mediaType[repo+"/manifests/"+dgst] = mediaType
}

func (r *registry) serve(w http.ResponseWriter, req *http.Request) {
	if strings.Contains(req.URL.Path, "/blobs/") {
		r.mu.Lock()
		r.blobReads++
		r.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	if req.URL.Path == "/v2/" || req.URL.Path == "/v2" {
		w.WriteHeader(http.StatusOK)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	if !strings.Contains(path, "/manifests/") {
		http.NotFound(w, req)
		return
	}
	if r.user != "" {
		user, pass, ok := req.BasicAuth()
		if !ok || user != r.user || pass != r.pass {
			r.mu.Lock()
			r.challenges++
			r.mu.Unlock()
			w.Header().Set("WWW-Authenticate", `Basic realm="kits"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	r.mu.Lock()
	body, ok := r.manifests[path]
	mediaType := r.mediaType[path]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
