package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/spec"
)

// Comment-descriptor examples get the same drift guard the YAML examples
// have in spec/examples_test.go: every example dockerfile carrying a
// `# kit:` block must extract, decode, and validate against the current
// grammar. The examples need not exercise the form at all — extraction
// itself is covered by comment_descriptor_test.go against fixtures — so
// this asserts only that the scan happened, not that it found anything.
func TestExampleCommentDescriptorsValidate(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "*.dockerfile"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no example dockerfiles scanned")

	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		yamlBytes, ok := extractCommentDescriptor(raw)
		if !ok {
			continue // a companion dockerfile, validated through its kit.yaml
		}
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			d, err := spec.Decode(yamlBytes)
			require.NoError(t, err)
			_, err = spec.ValidateRaw(yamlBytes, d)
			require.NoError(t, err)
		})
	}
}
