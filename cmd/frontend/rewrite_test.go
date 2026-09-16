package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// The same string can legitimately occur before the field — a description
// mentioning the file — and a textual first-occurrence replace rewrote
// the prose while the field kept pointing at the build context.
func TestRewriteContentFileTouchesTheFieldNotTheProse(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: mixin
displayName: Guidance
description: reads ./context.md at build time
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./context.md
`)

	out, err := rewriteContentFile(published, "./context.md", "/usr/share/sandbox/kit/demo/context.md")
	require.NoError(t, err)

	d, err := spec.Decode(out)
	require.NoError(t, err)
	require.Contains(t, d.Description, "./context.md", "prose must keep the authored path")

	ac, err := spec.AgentContextOf(d.Capabilities)
	require.NoError(t, err)
	require.NotNil(t, ac)
	require.Equal(t, "/usr/share/sandbox/kit/demo/context.md", ac.ContentFile)
}

func TestRewriteContentFileFailsWhenTheFieldIsAbsent(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: mixin
displayName: No guidance
`)
	_, err := rewriteContentFile(published, "./context.md", "/staged")
	require.ErrorContains(t, err, "could not rewrite")
}
