package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Index annotations are emitted exactly when the export produces an
// index. For a single platform that is decided by live attestations, so
// the gate must read the attest opts the way the solver does — including
// buildx's disabled=true spelling for --provenance=false.
func TestAttestationsRequested(t *testing.T) {
	require.False(t, attestationsRequested(map[string]string{}))
	require.True(t, attestationsRequested(map[string]string{"attest:provenance": "mode=min"}))
	require.True(t, attestationsRequested(map[string]string{"attest:sbom": ""}))
	require.False(t, attestationsRequested(map[string]string{"attest:provenance": "disabled=true"}))
	require.True(t, attestationsRequested(map[string]string{
		"attest:provenance": "disabled=true",
		"attest:sbom":       "",
	}))
	// buildx's default min provenance on exports that cannot carry
	// attestation manifests: inline in the image config, bare-manifest
	// export shape, no index to annotate.
	require.False(t, attestationsRequested(map[string]string{"attest:provenance": "mode=min,inline-only=true"}))
	require.True(t, attestationsRequested(map[string]string{
		"attest:provenance": "mode=min,inline-only=true",
		"attest:sbom":       "",
	}))
}
