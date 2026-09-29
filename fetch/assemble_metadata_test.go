package fetch

import (
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestAssembleEnvironmentConflictsDoNotExposeValues(t *testing.T) {
	for _, source := range []string{"image", "args within kit", "args across kits"} {
		t.Run(source, func(t *testing.T) {
			base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
			first, second := "secret-first-123", "secret-second-456"
			stage := StageResolve
			if source == "image" {
				base.Config.Config.Env = []string{"TOKEN=" + first}
				mixin.Config.Config.Env = []string{"TOKEN=" + second}
				stage = StageCompose
			} else {
				baseDescriptor := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
					Args: map[string]spec.Arg{"first": {Default: &first, Env: "TOKEN"}}}
				if source == "args within kit" {
					baseDescriptor.Args["second"] = spec.Arg{Default: &second, Env: "TOKEN"}
				} else {
					mixin.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
						Args: map[string]spec.Arg{"second": {Default: &second, Env: "TOKEN"}}})
				}
				base.Descriptor = kitJSON(t, baseDescriptor)
			}
			var events []Progress
			result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: ValidateLayers,
				Loader: fixtureLoader(base, mixin), OnProgress: func(p Progress) { events = append(events, p) },
			})
			require.Nil(t, result)
			require.ErrorContains(t, err, "TOKEN")
			require.ErrorContains(t, err, "example.com/kit0")
			if source != "args within kit" {
				require.ErrorContains(t, err, "example.com/kit1")
			}
			require.NotContains(t, err.Error(), first)
			require.NotContains(t, err.Error(), second)
			require.Equal(t, Progress{Stage: stage, State: ProgressFailed}, events[len(events)-1])
		})
	}
}

func TestAssembleRejectsZeroLayerKits(t *testing.T) {
	for index, kind := range []string{spec.KindWorkload, spec.KindMixin} {
		for _, reader := range []string{"nil reader", "present reader"} {
			t.Run(kind+"/"+reader, func(t *testing.T) {
				base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
				mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
				inputs := []*LoadedKit{base, mixin}
				inputs[index].Manifest.Layers = nil
				inputs[index].Config.RootFS.DiffIDs = nil
				if reader == "nil reader" {
					inputs[index].LayerLoader = nil
				}
				var events []Progress
				result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: ValidateLayers,
					Loader:     fixtureLoader(inputs...),
					OnProgress: func(p Progress) { events = append(events, p) },
				})
				require.Nil(t, result)
				require.ErrorContains(t, err, "kit manifest must contain at least one layer")
				require.ErrorContains(t, err, fixtureRequests(2)[index].Reference)
				require.Equal(t, Progress{Stage: StageLoad, State: ProgressFailed, Reference: fixtureRequests(2)[index].Reference}, events[len(events)-1])
			})
		}
	}
}

func TestAssembleValidatesEveryInputEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"missing separator", "SECRET"},
		{"empty name", "=secret-value"},
		{"NUL name", "SECRET\x00=secret-value"},
		{"NUL value", "SECRET=secret-value\x00"},
	} {
		for _, index := range []int{0, 1} {
			kind := []string{spec.KindWorkload, spec.KindMixin}[index]
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
				mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
				inputs := []*LoadedKit{base, mixin}
				// Even a later valid value must not hide an invalid earlier one.
				inputs[index].Config.Config.Env = []string{tc.entry, "SECRET=valid"}
				var events []Progress
				result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: ValidateLayers,
					Loader:     fixtureLoader(inputs...),
					OnProgress: func(p Progress) { events = append(events, p) },
				})
				require.Nil(t, result)
				require.ErrorContains(t, err, "malformed image environment entry at index 0")
				require.NotContains(t, err.Error(), "secret-value")
				require.Equal(t, Progress{Stage: StageLoad, State: ProgressFailed, Reference: fixtureRequests(2)[index].Reference}, events[len(events)-1])
			})
		}
	}
}

func TestAssemblePreservesValidEnvironmentValues(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	base.Config.Config.Env = []string{"EMPTY=", "EQUALS=a=b", "DUP=first", "DUP=last"}
	mixin.Config.Config.Env = []string{"MIXIN_EMPTY=", "MIXIN_EQUALS=c=d"}
	result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: ValidateLayers, Loader: fixtureLoader(base, mixin)})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"EMPTY": "", "EQUALS": "a=b", "DUP": "last", "MIXIN_EMPTY": "", "MIXIN_EQUALS": "c=d"}, result.Environment)
}

func TestAssembleManifestWithoutEmbeddedMediaType(t *testing.T) {
	for _, problem := range []string{"none", "schema version", "artifact type", "config type", "explicit manifest type"} {
		t.Run(problem, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			input.Manifest.MediaType = ""
			switch problem {
			case "schema version":
				input.Manifest.SchemaVersion = 1
			case "artifact type":
				input.Manifest.ArtifactType = "application/example"
			case "config type":
				input.Manifest.Config.MediaType = "application/example"
			case "explicit manifest type":
				input.Manifest.MediaType = "application/example"
			}
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: ValidateLayers, Loader: fixtureLoader(input)})
			if problem != "none" {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Empty(t, input.Manifest.MediaType, "validation must not rewrite loaded metadata")
		})
	}
}
