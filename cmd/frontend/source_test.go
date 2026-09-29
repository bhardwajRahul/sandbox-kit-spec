package main

import (
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/errdefs"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestYAMLSourceIncludesEveryValidationFailure(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: 99999}
args:
  version:
    pattern: '['
`)
	d, err := spec.Decode(raw)
	require.NoError(t, err)
	_, err = spec.ValidateRaw(raw, d)
	located := withYAMLSource(err, "kit.yaml", raw)
	var all spec.ValidationErrors
	require.ErrorAs(t, located, &all)
	require.Len(t, all, 2)
	sources := errdefs.Sources(located)
	require.Len(t, sources, 1)
	require.Equal(t, "kit.yaml", sources[0].Info.Filename)
	require.Equal(t, raw, sources[0].Info.Data)
	require.Len(t, sources[0].Ranges, 2)
	require.EqualValues(t, 5, sources[0].Ranges[0].Start.Line)
	require.EqualValues(t, 8, sources[0].Ranges[1].Start.Line)
	require.EqualError(t, located, err.Error(), "BuildKit owns source rendering; messages stay plain")
	var rendered strings.Builder
	require.NoError(t, sources[0].Print(&rendered))
	require.Contains(t, rendered.String(), ">>>     config:")
	require.Contains(t, rendered.String(), ">>>     pattern:")
	rendered.WriteString(located.Error())
	require.Equal(t, 1, strings.Count(rendered.String(), "config: {container: 99999}"))
	require.Equal(t, 1, strings.Count(rendered.String(), "pattern: '['"))
}

func TestYAMLSourceMissingFieldAndDecodeFailure(t *testing.T) {
	raw := []byte("schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - type: com.docker.sandbox/port@1\n    config: {}\n")
	d, err := spec.Decode(raw)
	require.NoError(t, err)
	_, err = spec.Validate(d)
	sources := errdefs.Sources(withYAMLSource(err, "kit.yaml", raw))
	require.Len(t, sources, 1)
	require.Len(t, sources[0].Ranges, 1)
	require.EqualValues(t, 5, sources[0].Ranges[0].Start.Line)

	raw = []byte("schemaVersion: [")
	_, err = spec.Decode(raw)
	located := withYAMLSource(err, "kit.yaml", raw)
	require.EqualError(t, located, err.Error())
	require.Equal(t, "kit.yaml", errdefs.Sources(located)[0].Info.Filename)
	require.Empty(t, errdefs.Sources(located)[0].Ranges)
	require.Nil(t, withYAMLSource(nil, "kit.yaml", raw))
}
