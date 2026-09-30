package fetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestAssembleLoaderImagePreservesConsumptionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, reference, version string
	}{
		{"directory", "./my-kit", "1.0.0"},
		{"absolute directory", "/kits/my-kit", "1.0.0"},
		{"git tag", "git+https://example.com/kits.git#ref=v2.1.0&dir=base", "2.1.0"},
		{"git branch", "git+https://example.com/kits.git#ref=main&dir=base", "1.0.0"},
		{"registry override", "example.com/base:3.0.0", "3.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			input.Image = "local-kit-build:99.0.0"
			input.Descriptor = kitJSON(t, &spec.Descriptor{
				SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Version: "1.0.0",
				Provides: []string{"base"}, Args: map[string]spec.Arg{"message": {Required: true}},
				Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
					"files": []any{map[string]any{"path": "/message", "content": "${{ kit.args.message }}"}},
				}}},
			})
			result, err := Assemble(t.Context(), []Request{{Reference: tc.reference, Args: map[string]string{"message": "hello"}}}, Options{
				Loader: func(_ context.Context, ref string) (*LoadedKit, error) {
					require.Equal(t, tc.reference, ref)
					return input, nil
				},
				LayerValidator: DefaultLayerValidator,
			})
			require.NoError(t, err)
			require.Len(t, result.Resolved.Kits, 1)
			unit := result.Resolved.Kits[0]
			require.Equal(t, tc.reference, unit.Reference)
			require.Equal(t, input.Image, unit.Image, "the supplied image is used unchanged")
			require.Equal(t, input.Digest.String(), unit.Digest)
			require.Equal(t, tc.version, unit.Descriptor.Version, "version comes from consumption identity, not the build tag")
			selection := result.Resolved.Selections[0]
			require.Equal(t, tc.reference, selection.Reference)
			require.Equal(t, "1.0.0", selection.PublishedDescriptor.Version)
			require.Len(t, selection.Selection.Selected, 1)
			require.Equal(t, tc.reference, selection.Selection.Selected[0].Source.Kit)
			resolution, err := resolve.Resolve(result.Resolved.Kits)
			require.NoError(t, err)
			locked := resolve.LockFrom(resolution).Kits[0]
			require.Equal(t, tc.reference, locked.Reference)
			require.Equal(t, input.Image, locked.Image)
			require.Equal(t, input.Digest.String(), locked.Digest)
			require.Equal(t, map[string]string{"message": "hello"}, locked.Args)
			require.Equal(t, input.Manifest.Layers, result.Image.Layers)
		})
	}
}

func TestAssembleMixesRegistryAndSourceKits(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	tool, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Requires: []string{"tool >= 2.1.0, < 3.0.0"},
	})
	tool.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Version: "1.0.0", Provides: []string{"tool"},
	})
	tool.Image = "local-tool-build:99.0.0"
	registryRef := "example.com/base:1.0.0"
	sourceRef := "git+https://example.com/kits.git#ref=v2.1.0&dir=tool"
	inputs := map[string]*LoadedKit{registryRef: base, sourceRef: tool}
	result, err := Assemble(t.Context(), []Request{{Reference: registryRef}, {Reference: sourceRef}}, Options{
		Loader:         func(_ context.Context, ref string) (*LoadedKit, error) { return inputs[ref], nil },
		LayerValidator: DefaultLayerValidator,
	})
	require.NoError(t, err)
	require.Len(t, result.Resolved.Kits, 2)
	require.Equal(t, sourceRef, result.Resolved.Kits[0].Reference, "declarations follow dependencies")
	require.Equal(t, tool.Image, result.Resolved.Kits[0].Image)
	require.Equal(t, "example.com/base@"+base.Digest.String(), result.Resolved.Kits[1].Image, "empty Image keeps registry digest pinning")
	require.Equal(t, base.Manifest.Layers[0], result.Image.Layers[0], "layers start with the workload")
	require.Equal(t, tool.Manifest.Layers[0], result.Image.Layers[1])
}

func TestAssembleSourceErrorsUseConsumptionReference(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	input.Image = "local-kit-build:99.0.0"
	input.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Requires: []string{"missing"},
	})
	result, err := Assemble(t.Context(), []Request{{Reference: "./my-kit"}}, Options{
		Loader: func(context.Context, string) (*LoadedKit, error) { return input, nil },
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "./my-kit requires")
	require.NotContains(t, err.Error(), input.Image)
}

func TestUnitsRequireAnImageForSourceReferences(t *testing.T) {
	for _, ref := range []string{"./my-kit", "git+https://example.com/kits.git#ref=main"} {
		t.Run(ref, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			kit, err := validateLoadedKit(ref, input)
			require.NoError(t, err)
			units, err := Units([]*Kit{kit})
			require.Nil(t, units)
			require.ErrorContains(t, err, ref)
			kit.Image = "local-kit-build:99.0.0"
			units, err = Units([]*Kit{kit})
			require.NoError(t, err)
			require.Equal(t, ref, units[0].Reference)
			require.Equal(t, kit.Image, units[0].Image)
		})
	}
}
