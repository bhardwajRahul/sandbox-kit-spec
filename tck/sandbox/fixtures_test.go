package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Every fixture the suite composes must be a valid kit, or a conformance
// failure could be the fixture's fault rather than the runtime's.
func TestFixturesAreValidKits(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "fixtures", "*", "*.yaml"))
	require.NoError(t, err)
	require.Len(t, matches, 30, "every fixture directory needs its descriptor")

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			d, err := spec.Decode(raw)
			require.NoError(t, err)
			_, err = spec.ValidateRaw(raw, d)
			require.NoError(t, err)
		})
	}
}
