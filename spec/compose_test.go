package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComposeAndPublishKeepDifferentContextRepresentations(t *testing.T) {
	inputs := []Contribution{
		contribute(t, "tool", `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/agent-context@1
    name: Tool guidance
    optional: true
    config:
      content: Use the tool.
`),
		contribute(t, "agent", `schemaVersion: "3"
kind: workload
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: AGENTS.md
      contentFile: /usr/share/sandbox/kit/agent/context.md
`),
	}
	before, err := json.Marshal(inputs)
	require.NoError(t, err)
	runtime, err := Compose(inputs)
	require.NoError(t, err)
	profile, err := AgentContextOf(runtime.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &AgentContext{Filename: "AGENTS.md"}, profile)
	require.Equal(t, "Tool guidance", runtime.Capabilities[0].Name)
	require.False(t, runtime.Capabilities[0].Optional)

	_, err = Merge(inputs, MergeOptions{})
	require.ErrorContains(t, err, "2 agent-context bodies to stage but no ContextPath")
	published, err := Merge(inputs, MergeOptions{ContextPath: "/usr/share/sandbox/kit/set/context.md"})
	require.NoError(t, err)
	staged, err := AgentContextOf(published.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "AGENTS.md", staged.Filename)
	require.Equal(t, "/usr/share/sandbox/kit/set/context.md", staged.ContentFile)
	require.Equal(t, []ContextSource{
		{Reference: "tool", Content: "Use the tool."},
		{Reference: "agent", Path: "/usr/share/sandbox/kit/agent/context.md"},
	}, published.ContextSources)
	require.Equal(t, runtime.Capabilities[0].Name, published.Descriptor.Capabilities[0].Name)
	require.False(t, published.Descriptor.Capabilities[0].Optional)

	after, err := json.Marshal(inputs)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "composition and publishing leave their inputs intact")
	require.NotContains(t, runtime.Capabilities[0].Config, "contentFile")
}

func TestComposeContextPresence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]any
		keep   bool
	}{
		{name: "no request", config: map[string]any{}},
		{name: "profile only", config: map[string]any{"filename": "AGENTS.md"}, keep: true},
		{name: "body only", config: map[string]any{"content": "Guidance"}, keep: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Compose([]Contribution{{Reference: "agent", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindWorkload,
				Capabilities: []Capability{{Type: CapabilityAgentContext, Name: "Guidance", Optional: true, Config: tt.config}},
			}}})
			require.NoError(t, err)
			if !tt.keep {
				require.Empty(t, result.Capabilities)
				return
			}
			require.Len(t, result.Capabilities, 1)
			require.True(t, result.Capabilities[0].Optional)
			require.Equal(t, "Guidance", result.Capabilities[0].Name)
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, result)
			require.NoError(t, err)
		})
	}
}
