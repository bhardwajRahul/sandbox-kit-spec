package spec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every descriptor under examples/ must decode and validate against the
// current grammar; an example the spec has drifted away from is worse
// than none.
func TestExamplesDecodeAndValidate(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "examples", "*", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.NoError(t, err)
		})
	}
}
