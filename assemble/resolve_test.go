package assemble

import (
	"context"
	"errors"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func resolvedUnit(name, kind string) *resolve.Unit {
	return &resolve.Unit{Reference: name, Image: "store/" + name, Descriptor: &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: kind, Provides: []string{name + "@1.0.0"},
	}}
}

func TestAssembleLoadsWorkloadBeforeItsDependencies(t *testing.T) {
	workload := resolvedUnit("workload", spec.KindWorkload)
	workload.Descriptor.Requires = []string{"tool"}
	tool := resolvedUnit("tool", spec.KindMixin)
	var loaded []string
	load := func(ctx context.Context, ref string) (Input, error) {
		loaded = append(loaded, ref)
		in := input(ref, ref)
		in.Config.Platform = ocispec.Platform{OS: "linux", Architecture: "amd64"}
		in.Config.Config.Entrypoint = []string{ref}
		return in, nil
	}
	image, err := Assemble(t.Context(), []*resolve.Unit{tool, workload}, load)
	require.NoError(t, err)
	require.Equal(t, []string{workload.Image, tool.Image}, loaded)
	require.Equal(t, []string{workload.Image}, image.Config.Config.Entrypoint)
	baseLayer, _ := layerDesc(workload.Image)
	require.Equal(t, baseLayer, image.Layers[0])
	require.Len(t, image.Layers, 2)
}

func TestAssembleSingleKit(t *testing.T) {
	workload := resolvedUnit("workload", spec.KindWorkload)
	base := input("base", "rootfs")
	base.Config.Config.Env = []string{"TEAM=default"}
	image, err := Assemble(t.Context(), []*resolve.Unit{workload}, func(context.Context, string) (Input, error) { return base, nil })
	require.NoError(t, err)
	require.Equal(t, base.Config.Config, image.Config.Config)
	require.Equal(t, base.Config.RootFS, image.Config.RootFS)
	require.Empty(t, image.Config.History)
	require.Equal(t, base.Manifest.Layers, image.Layers)
}

func TestAssembleRejectsInvalidSetsBeforeLoading(t *testing.T) {
	workload := resolvedUnit("workload", spec.KindWorkload)
	for _, tt := range []struct {
		name string
		kits []*resolve.Unit
	}{
		{"empty", nil},
		{"nil kit", []*resolve.Unit{nil}},
		{"missing descriptor", []*resolve.Unit{{Reference: "broken", Image: "store/broken"}}},
		{"missing image", []*resolve.Unit{{Reference: "broken", Descriptor: workload.Descriptor}}},
		{"no workload", []*resolve.Unit{resolvedUnit("tool", spec.KindMixin)}},
		{"two workloads", []*resolve.Unit{workload, resolvedUnit("other", spec.KindWorkload)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			image, err := Assemble(t.Context(), tt.kits, func(context.Context, string) (Input, error) {
				t.Fatal("invalid set reached loader")
				return Input{}, nil
			})
			require.Error(t, err)
			require.Nil(t, image)
		})
	}
}

func TestAssembleLoaderFailures(t *testing.T) {
	workload := resolvedUnit("workload", spec.KindWorkload)
	_, err := Assemble(t.Context(), []*resolve.Unit{workload}, nil)
	require.ErrorContains(t, err, "no image loader")
	failure := errors.New("store unavailable")
	_, err = Assemble(t.Context(), []*resolve.Unit{workload}, func(context.Context, string) (Input, error) { return Input{}, failure })
	require.ErrorIs(t, err, failure)
	require.ErrorContains(t, err, workload.Reference)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Assemble(ctx, []*resolve.Unit{workload}, func(context.Context, string) (Input, error) {
		t.Fatal("canceled request reached loader")
		return Input{}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	_, err = Assemble(t.Context(), []*resolve.Unit{workload, resolvedUnit("tool", spec.KindMixin)}, func(_ context.Context, ref string) (Input, error) {
		in := input(ref, "layer")
		in.Config.Platform = ocispec.Platform{OS: "linux", Architecture: "amd64"}
		if ref != workload.Image {
			in.Config.Architecture = "arm64"
		}
		return in, nil
	})
	require.ErrorContains(t, err, "tool is linux/arm64 but workload workload is linux/amd64")
}
