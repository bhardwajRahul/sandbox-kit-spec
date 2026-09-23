package main

import (
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// dockerfileFrontendOpts forwarded only target/label/build-arg options, so
// --no-cache, --pull, cache imports, network mode, and named contexts were
// silently dropped on the way into the companion's dockerfile.v0 sub-solve.
// The forwarding is a denylist now: everything passes except the keys this
// frontend owns (filename, platform), the dispatch escape hatch
// (BUILDKIT_SYNTAX), and the keys that would shape the sub-result as
// multi-ref, which SingleRef refuses (multi-platform, attestations).
func TestDockerfileFrontendOptsForwarding(t *testing.T) {
	amd64 := ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	def := "def"

	for _, tt := range []struct {
		name string
		d    *spec.Descriptor
		opts map[string]string
		plat *ocispecs.Platform
		want map[string]string
	}{
		{
			name: "no-cache for all stages survives with its empty value",
			d:    &spec.Descriptor{},
			opts: map[string]string{"no-cache": ""},
			want: map[string]string{"no-cache": "", "filename": "kit.build.dockerfile"},
		},
		{
			name: "cache, network, resolve-mode, hosts, and named contexts pass through",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"no-cache":           "install,test",
				"cache-imports":      `[{"Type":"registry","Attrs":{"ref":"example.com/cache"}}]`,
				"image-resolve-mode": "pull",
				"force-network-mode": "none",
				"add-hosts":          "example.com=203.0.113.7",
				"context:deps":       "docker-image://alpine:3.20",
			},
			want: map[string]string{
				"no-cache":           "install,test",
				"cache-imports":      `[{"Type":"registry","Attrs":{"ref":"example.com/cache"}}]`,
				"image-resolve-mode": "pull",
				"force-network-mode": "none",
				"add-hosts":          "example.com=203.0.113.7",
				"context:deps":       "docker-image://alpine:3.20",
				"filename":           "kit.build.dockerfile",
			},
		},
		{
			name: "previously allowlisted keys keep passing",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"target":         "runtime",
				"label:org.team": "kits",
				"build-arg:FOO":  "bar",
			},
			want: map[string]string{
				"target":         "runtime",
				"label:org.team": "kits",
				"build-arg:FOO":  "bar",
				"filename":       "kit.build.dockerfile",
			},
		},
		{
			name: "filename and platform are this frontend's, not the caller's",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"filename": "kit.yaml",
				"platform": "linux/amd64,linux/arm64",
			},
			plat: &amd64,
			want: map[string]string{
				"filename": "kit.build.dockerfile",
				"platform": "linux/amd64",
			},
		},
		{
			name: "BUILDKIT_SYNTAX never re-dispatches the companion into this frontend",
			d:    &spec.Descriptor{},
			opts: map[string]string{"build-arg:BUILDKIT_SYNTAX": "docker/sandbox-kit"},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "multi-platform and attestation requests stay out of the sub-solve",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"multi-platform":                    "true",
				"build-arg:BUILDKIT_MULTI_PLATFORM": "true",
				"attest:sbom":                       "",
				"attest:provenance":                 "mode=max",
				"build-arg:BUILDKIT_ATTEST_SBOM":    "",
			},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "kit args map onto their declared build-arg names",
			d: &spec.Descriptor{Args: map[string]spec.Arg{
				"version": {BuildArg: "APP_VERSION"},
				"channel": {BuildArg: "APP_CHANNEL", Default: &def},
			}},
			opts: map[string]string{"build-arg:version": "1.2.3"},
			want: map[string]string{
				"build-arg:version":     "1.2.3",
				"build-arg:APP_VERSION": "1.2.3",
				"build-arg:APP_CHANNEL": "def",
				"filename":              "kit.build.dockerfile",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := dockerfileFrontendOpts(tt.d, tt.opts, "kit.build.dockerfile", tt.plat)
			require.Equal(t, tt.want, got)
		})
	}
}
