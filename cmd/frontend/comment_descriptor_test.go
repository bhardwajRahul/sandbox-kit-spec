package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/spec"
)

const commentKitFile = `# syntax=docker/sandbox-kit:3
# Prose above the marker stays prose.
#
# kit:
#   schemaVersion: "3"
#   kind: mixin
#   provides: ["gh@2.98.0"]
#
#   capabilities:
#     - type: com.docker.sandbox/network-policy@1
#       config:
#         runtime:
#           allow: [github.com]

FROM debian:trixie-slim AS build
RUN echo hi > /out/gh

FROM scratch
COPY --from=build /out/ /
`

// The extracted block is byte-faithful YAML (dedent only) that decodes
// and validates as a descriptor.
func TestExtractCommentDescriptor(t *testing.T) {
	yamlBytes, ok := extractCommentDescriptor([]byte(commentKitFile))
	require.True(t, ok)

	d, err := spec.Decode(yamlBytes)
	require.NoError(t, err)
	require.Equal(t, spec.KindMixin, d.Kind)
	require.Equal(t, []string{"gh@2.98.0"}, d.Provides)
	_, err = spec.ValidateRaw(yamlBytes, d)
	require.NoError(t, err)

	policy, err := spec.NetworkPolicyOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"github.com"}, policy.Runtime.Allow)
}

// The block ends at the first non-comment line; trailing prose comments
// under-indented relative to the marker end it too, so no explicit
// terminator is needed.
func TestExtractCommentDescriptorBoundaries(t *testing.T) {
	trailingProse := `# kit:
#   schemaVersion: "3"
#   kind: mixin
#   provides: ["x@1.0.0"]
# This prose comment is not indented under the marker and ends the block.
FROM scratch
`
	yamlBytes, ok := extractCommentDescriptor([]byte(trailingProse))
	require.True(t, ok)
	d, err := spec.Decode(yamlBytes)
	require.NoError(t, err)
	require.Equal(t, []string{"x@1.0.0"}, d.Provides)
}

// No marker means not this authoring form — never a false positive on
// ordinary Dockerfiles.
func TestExtractCommentDescriptorAbsent(t *testing.T) {
	_, ok := extractCommentDescriptor([]byte("FROM scratch\n# just a comment\n"))
	require.False(t, ok)
}

// A YAML descriptor is never misrouted: Build tries Decode first, and
// extraction only runs on files that fail it. This pins the supporting
// invariant that a valid descriptor decodes even when its own comments
// contain the marker.
func TestYAMLDescriptorWithMarkerCommentStillDecodes(t *testing.T) {
	y := `# syntax=docker/sandbox-kit:3
# kit:
schemaVersion: "3"
kind: mixin
provides: ["x@1.0.0"]
`
	d, err := spec.Decode([]byte(y))
	require.NoError(t, err)
	require.Equal(t, []string{"x@1.0.0"}, d.Provides)
}

func TestNeutralizeSyntaxLine(t *testing.T) {
	out := neutralizeSyntaxLine([]byte("# syntax=docker/sandbox-kit:3\nFROM scratch\n"))
	require.Equal(t, "# (syntax directive consumed by the kit frontend)\nFROM scratch\n", string(out))

	untouched := []byte("FROM scratch\n# syntax=not-a-directive-here\n")
	require.Equal(t, untouched, neutralizeSyntaxLine(untouched))
}

// A second syntax stanza in the leading comment region declares the
// content's own frontend: the content starts there, so the stanza is
// line 1 of the handoff — the only position directives are honored.
func TestContentRecipeSecondSyntaxStanza(t *testing.T) {
	file := `# syntax=docker/sandbox-kit:3
# kit:
#   schemaVersion: "3"
#   kind: mixin
#   provides: ["x@1.0.0"]

# syntax=docker/dockerfile:1.7-labs
FROM scratch
`
	got := contentRecipe([]byte(file))
	require.Equal(t, "# syntax=docker/dockerfile:1.7-labs\nFROM scratch\n", string(got))
}

// Without a second stanza the content is the whole file with the leading
// directive neutralized — never forwarded, never recursive.
func TestContentRecipeNoSecondStanza(t *testing.T) {
	got := contentRecipe([]byte(commentKitFile))
	require.Contains(t, string(got), "# (syntax directive consumed by the kit frontend)\n")
	require.Contains(t, string(got), "FROM debian:trixie-slim AS build\n")
	require.NotContains(t, string(got), "# syntax=docker/sandbox-kit:3")
}

// A syntax-shaped comment below the first instruction is not a directive
// in any reading of the file; slicing there would change what builds.
func TestContentRecipeIgnoresStanzaAfterInstruction(t *testing.T) {
	file := "# syntax=docker/sandbox-kit:3\n# kit:\n#   schemaVersion: \"3\"\nFROM scratch\n# syntax=docker/dockerfile:1\nRUN true\n"
	got := contentRecipe([]byte(file))
	require.True(t, strings.HasPrefix(string(got), "# (syntax directive consumed by the kit frontend)\n"))
	require.Contains(t, string(got), "# syntax=docker/dockerfile:1\n")
}

// The BUILDKIT_SYNTAX dispatch case: no leading directive at all, but a
// stanza in the comment region still marks the content start.
func TestContentRecipeStanzaWithoutLeadingDirective(t *testing.T) {
	file := "# kit:\n#   schemaVersion: \"3\"\n\n# syntax=docker/dockerfile:1\nFROM scratch\n"
	got := contentRecipe([]byte(file))
	require.Equal(t, "# syntax=docker/dockerfile:1\nFROM scratch\n", string(got))
}
