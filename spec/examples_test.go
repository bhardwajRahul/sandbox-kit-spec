package spec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every descriptor this repository ships must decode and validate against
// the current grammar; one the spec has drifted away from is worse than
// none.
//
// The glob covers examples/ and the root-level kits beside it. A kit that
// has to live next to the content it ships — skills/ is the one today —
// sits outside examples/ and would otherwise have no coverage at all, so a
// grammar change could break a published kit with every test still green.
func TestExamplesDecodeAndValidate(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "examples", "*", "*.yaml"))
	require.NoError(t, err)
	rootKits, err := filepath.Glob(filepath.Join("..", "skills", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, rootKits, "the skills kit descriptor must be covered")
	matches = append(matches, rootKits...)
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
