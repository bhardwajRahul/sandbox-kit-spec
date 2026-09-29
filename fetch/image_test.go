package fetch

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func (r *registry) imageWithConfig(t *testing.T, repo string, descriptor []byte, config ocispec.Image) []byte {
	t.Helper()
	var manifest ocispec.Manifest
	require.NoError(t, json.Unmarshal(r.image(t, descriptor), &manifest))
	manifest.Layers[0].Digest = digest.FromString(repo + config.Architecture)
	config.RootFS = ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromString(repo + config.Architecture + "-diff")}}
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	manifest.Config.Digest = digest.FromBytes(raw)
	manifest.Config.Size = int64(len(raw))
	r.mu.Lock()
	r.blobs[repo+"/blobs/"+manifest.Config.Digest.String()] = raw
	r.mu.Unlock()
	body, err := json.Marshal(manifest)
	require.NoError(t, err)
	return body
}

func TestResolveAndAssembleUsesPinnedImagesAndSeparateContainerEnv(t *testing.T) {
	reg := newRegistry(t)
	reg.user, reg.pass = "user", "password"
	team := "alpha"
	requests := make([]Request, 0, 2)
	for i, name := range []string{"workload", "tool"} {
		d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Provides: []string{name + "@1.0.0"}}
		config := ocispec.Image{Platform: defaultPlatform()}
		if i == 0 {
			d.Kind, d.Requires = spec.KindWorkload, []string{"tool"}
			d.Args = map[string]spec.Arg{"team": {Default: &team, Env: "TEAM"}}
			config.Config.Entrypoint = []string{"/bin/sh"}
			config.Config.Env = []string{"TEAM=image-default"}
		}
		reg.tag(name, "1.0.0", reg.imageWithConfig(t, name, kitJSON(t, d), config))
		requests = append(requests, Request{Reference: reg.ref(name, "1.0.0")})
	}
	client, err := New(WithCredential(auth.StaticCredential(reg.Listener.Addr().String(), auth.Credential{Username: reg.user, Password: reg.pass})))
	require.NoError(t, err)
	resolved, err := client.Resolve(t.Context(), requests)
	require.NoError(t, err)
	require.Equal(t, requests[1].Reference, resolved.Kits[0].Reference, "declarations follow dependencies")
	require.Zero(t, reg.blobReads)
	// Moving a tag between resolution and image loading must not change the input.
	reg.tag("workload", "1.0.0", reg.image(t, nil))
	image, err := assemble.Assemble(t.Context(), resolved.Kits, client.LoadImage)
	require.NoError(t, err)
	require.Equal(t, []string{"/bin/sh"}, image.Config.Config.Entrypoint)
	require.Equal(t, []string{"TEAM=image-default"}, image.Config.Config.Env)
	require.Equal(t, map[string]string{"TEAM": "alpha"}, resolved.ContainerEnv)
	require.Equal(t, digest.FromString("workload"+defaultPlatform().Architecture), image.Layers[0].Digest, "layers start with the workload")
	require.Len(t, image.Layers, 2)
	require.Equal(t, 2, reg.blobReads, "only config blobs are read")
	manifest, err := image.Manifest()
	require.NoError(t, err)
	raw, err := json.Marshal(image.Config)
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(raw), manifest.Config.Digest)
}

func TestLoadImageReachesPlatformManifestThroughAnnotatedIndex(t *testing.T) {
	reg := newRegistry(t)
	var children []ocispec.Descriptor
	for _, arch := range []string{"amd64", "arm64"} {
		platform := ocispec.Platform{OS: "linux", Architecture: arch}
		raw := reg.imageWithConfig(t, "kit", nil, ocispec.Image{Platform: platform})
		reg.tag("kit", arch, raw)
		children = append(children, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(raw), Size: int64(len(raw)), Platform: &platform})
	}
	nested := reg.index(t, children, nil)
	reg.tag("kit", "nested", nested)
	root := reg.index(t, []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageIndex, Digest: digest.FromBytes(nested), Size: int64(len(nested))}}, map[string]string{spec.AnnotationDescriptor: `{"schemaVersion":"3","kind":"workload"}`})
	reg.tag("kit", "latest", root)
	client, err := New(WithPlatform(ocispec.Platform{OS: "linux", Architecture: "arm64"}))
	require.NoError(t, err)
	input, err := client.LoadImage(t.Context(), reg.ref("kit", "latest"))
	require.NoError(t, err)
	require.Equal(t, "arm64", input.Config.Architecture)
	require.Equal(t, 1, reg.blobReads)
}

func TestLoadImageRejectsInvalidConfig(t *testing.T) {
	for _, problem := range []string{"platform", "digest", "size", "rootfs", "json"} {
		t.Run(problem, func(t *testing.T) {
			reg := newRegistry(t)
			raw := reg.imageWithConfig(t, "kit", nil, ocispec.Image{Platform: defaultPlatform()})
			var manifest ocispec.Manifest
			require.NoError(t, json.Unmarshal(raw, &manifest))
			blob := reg.blobs["kit/blobs/"+manifest.Config.Digest.String()]
			var config ocispec.Image
			require.NoError(t, json.Unmarshal(blob, &config))
			message := ""
			switch problem {
			case "platform":
				config.OS = "windows"
				message = "requested linux/"
			case "rootfs":
				config.RootFS.DiffIDs = nil
				message = "rootfs does not match"
			case "size":
				manifest.Config.Size = maxMetadataBytes + 1
				message = "outside the metadata budget"
			case "digest":
				manifest.Config.Digest = digest.FromString("wrong")
				message = "digest mismatch"
			case "json":
				message = "parse image config"
			}
			if problem == "platform" || problem == "rootfs" || problem == "json" {
				blob, err := json.Marshal(config)
				require.NoError(t, err)
				if problem == "json" {
					blob = []byte("not-json")
				}
				manifest.Config.Digest, manifest.Config.Size = digest.FromBytes(blob), int64(len(blob))
				reg.blobs["kit/blobs/"+manifest.Config.Digest.String()] = blob
			} else {
				reg.blobs["kit/blobs/"+manifest.Config.Digest.String()] = blob
			}
			raw, err := json.Marshal(manifest)
			require.NoError(t, err)
			reg.tag("kit", "latest", raw)
			client, err := New()
			require.NoError(t, err)
			_, err = client.LoadImage(t.Context(), reg.ref("kit", "latest"))
			require.ErrorContains(t, err, message, fmt.Sprintf("case %s", problem))
		})
	}
}
