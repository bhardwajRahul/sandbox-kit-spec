package main

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

// A staged context body lands in the same directory as the kit's own
// sources, so these two basenames would replace the descriptor a composed
// sandbox reads to learn what the kit declared.
func TestStagedGuidanceCollides(t *testing.T) {
	for _, name := range []string{"./kit.yaml", "kit.yaml", "docs/kit.dockerfile"} {
		require.True(t, stagedGuidanceCollides(name), "%q must be refused", name)
	}
	for _, name := range []string{"./context.md", "kit-context.md", "./kit.yaml.md"} {
		require.False(t, stagedGuidanceCollides(name), "%q is fine", name)
	}
}

// A missing path crosses the gateway as a message rather than an fs error,
// and reading it as a real failure would fail builds for files the checks
// merely asked about.
func TestIsNotExist(t *testing.T) {
	require.True(t, isNotExist(fs.ErrNotExist))
	require.True(t, isNotExist(errors.New(`failed to stat /usr/share/sandbox/kit/x/kit.yaml: no such file or directory`)))
	require.False(t, isNotExist(errors.New("permission denied")))
}

// A declaration-only kit's solve produces no reference at all, so the
// checks have to get "absent" rather than a nil dereference.
func TestBuildArtifactReadsNothingWithoutAReference(t *testing.T) {
	a := &buildArtifact{}
	body, ok, err := a.ReadFile(context.Background(), "/usr/share/sandbox/kit/x/kit.yaml")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, body)

	_, known, err := a.Layers(context.Background())
	require.NoError(t, err)
	require.False(t, known, "layers do not exist until the exporter makes them")
}
