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

// The annotation carries the JSON and the layers stage the YAML, and the
// staged-sources check compares them as documents — so an amendment that
// reached only one of them would fail the build it just assembled.
func TestDerivedProvidesLandInBothTheBytesAndTheDocument(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: workload
displayName: Shell
version: "1.0.0"
# What the author claims, which the derivation leaves alone.
provides:
  - shell
`)

	out, pd, err := withDerivedProvides(published, []string{"deb/bash@5.2.37", "deb/jq@1.8.2"})
	require.NoError(t, err)
	require.Equal(t, []string{"shell", "deb/bash@5.2.37", "deb/jq@1.8.2"}, pd.Provides,
		"authored entries keep their place and their order")

	fromBytes, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, pd.Provides, fromBytes.Provides, "the staged copy must say what the annotation says")
	require.Contains(t, string(out), "# What the author claims",
		"the staged copy is what a reader finds in the image, so comments survive")
}

// A kit that declared nothing still carries whatever its base image put
// there, so the field is created rather than skipped.
func TestDerivedProvidesCreateTheFieldWhenThereIsNone(t *testing.T) {
	for _, published := range []string{
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\n",
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\nprovides:\n",
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\nprovides: []\n",
	} {
		out, pd, err := withDerivedProvides([]byte(published), []string{"apk/musl@1.2.5"})
		require.NoError(t, err, published)
		require.Equal(t, []string{"apk/musl@1.2.5"}, pd.Provides, published)

		fromBytes, err := spec.Decode(out)
		require.NoError(t, err, published)
		require.Equal(t, pd.Provides, fromBytes.Provides, published)
	}
}

// The entries come out of a base image nobody here controls, so the
// assembled document is held to the published rules rather than trusted.
func TestDerivedProvidesAreValidatedNotTrusted(t *testing.T) {
	published := []byte("schemaVersion: \"3\"\nkind: workload\ndisplayName: Shell\nversion: \"1.0.0\"\n")

	_, _, err := withDerivedProvides(published, []string{"deb/Not A Name@1.0.0"})
	require.Error(t, err)
	require.ErrorContains(t, err, "derived from image content")
}
