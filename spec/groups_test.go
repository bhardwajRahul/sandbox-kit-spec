package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func groupHook(text string) Capability {
	return Capability{Type: CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": text}}}}
}

func TestGroupGrammar(t *testing.T) {
	for _, item := range []string{
		"group: {}",
		"group: {capabilities: []}",
		"group: null",
		"type: com.example/feature@1\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"optional: false\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"group: {name: 12, capabilities: [{type: com.example/member@1}]}",
		"group: {unknown: true, capabilities: [{type: com.example/member@1}]}",
		"group: {capabilities: [{type: com.example/member@1, unknown: true}]}",
		"group: {capabilities: [{type: com.example/member@1, optional: false}]}",
		"group: {capabilities: [{type: com.example/member@1, optional: true}]}",
		"group: {capabilities: [{group: {capabilities: [{type: com.example/member@1}]}}]}",
		"group: {capabilities: [{type: com.docker.sandbox/volume@1, config: {path: relative}}]}",
	} {
		t.Run(item, func(t *testing.T) {
			raw := []byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - " + strings.ReplaceAll(item, "\n", "\n    ") + "\n")
			d, err := Decode(raw)
			if err == nil {
				_, err = ValidateRaw(raw, d)
			}
			require.Error(t, err)
			var v any
			require.NoError(t, yaml.Unmarshal(raw, &v))
			encoded, err := json.Marshal(v)
			require.NoError(t, err)
			d, err = Decode(encoded)
			if err == nil {
				_, err = ValidateRaw(encoded, d)
			}
			require.Error(t, err)
		})
	}
}

func TestGroupValidationPathsAndBlocks(t *testing.T) {
	d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{groupHook("base"), {Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{groupHook("selected"), {Type: CapabilityVolume, Config: map[string]any{"path": "relative"}}}}}}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[1].config.path")
	d.Capabilities[1].Group.Capabilities = d.Capabilities[1].Group.Capabilities[:1]
	_, err = Validate(d)
	require.NoError(t, err, "singleton arity is per declaration block")
	d.Capabilities = append(d.Capabilities, groupHook("duplicate"))
	_, err = Validate(d)
	require.ErrorContains(t, err, "capabilities[2].type")
	d.Capabilities = d.Capabilities[:2]
	d.Capabilities[1].Group.Capabilities = append(d.Capabilities[1].Group.Capabilities, groupHook("duplicate"))
	_, err = Validate(d)
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[1].type")
}

func TestSelectCapabilitiesAtomicAndOrdered(t *testing.T) {
	items := []Capability{groupHook("before"), {Group: &CapabilityGroup{Name: "feature", Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, groupHook("inside")}}},
		{Group: &CapabilityGroup{Name: "feature", Capabilities: []Capability{{Type: CapabilityVolume, Config: map[string]any{"path": "/data"}}}}}}
	var calls []string
	selected, err := SelectCapabilities(items, func(c Capability) bool { calls = append(calls, c.Type); return c.Type != "com.example/feature@1" })
	require.NoError(t, err)
	require.Len(t, calls, 4, "all members evaluated for rejection diagnostics")
	require.Len(t, selected.Capabilities, 2)
	require.Len(t, selected.Selected, 2)
	require.Len(t, selected.Skipped, 1)
	require.Equal(t, []string{"capabilities[1].group.capabilities[0]"}, selected.Skipped[0].Rejected)
	require.Equal(t, "capabilities[2].group.capabilities[0]", selected.Capabilities[1].Source.Path)
	surface := SurfaceOf(&Descriptor{Capabilities: selected.Capabilities})
	require.Equal(t, []string{"/data"}, surface.StoragePaths)
	require.Empty(t, surface.Services)
	accepted, err := SelectCapabilities(items, Supported(CapabilityLifecycle, CapabilityVolume, "com.example/feature@1"))
	require.NoError(t, err)
	require.Len(t, accepted.Capabilities, 4)
	require.Equal(t, CapabilityLifecycle, accepted.Capabilities[2].Type)
	items[1].Group.Optional = false
	refused, err := SelectCapabilities(items, Supported(CapabilityLifecycle, CapabilityVolume))
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[0]")
	require.Len(t, refused.Skipped, 1)
	require.Len(t, items[1].Group.Capabilities, 2, "selection does not mutate declarations")
}

func TestSelectionValidatesBeforeCallingRuntime(t *testing.T) {
	for _, c := range []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "relative"}},
		{Type: CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}},
	} {
		_, err := SelectCapabilities([]Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{c}}}}, func(Capability) bool { t.Fatal("invalid declarations must not reach runtime selection"); return false })
		require.Error(t, err)
	}
	_, err := SelectCapabilities(nil, nil)
	require.Error(t, err)
}

func TestSelectedCompositionAndPublishedOrder(t *testing.T) {
	a := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{groupHook("same"), {Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, groupHook("inside")}}}}}
	b := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{groupHook("same")}}
	inputs := []Contribution{{Reference: "a", Descriptor: a}, {Reference: "b", Descriptor: b}}
	published, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(published.Descriptor)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, decoded)
	require.NoError(t, err)
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			selector := func(c Capability) bool { return accept || c.Type != "com.example/feature@1" }
			var direct []Contribution
			for _, input := range inputs {
				selection, err := SelectCapabilities(input.Descriptor.Capabilities, selector)
				require.NoError(t, err)
				d := *input.Descriptor
				d.Capabilities = selection.Capabilities
				direct = append(direct, Contribution{Reference: input.Reference, Descriptor: &d})
			}
			merged, err := Compose(direct)
			require.NoError(t, err)
			selection, err := SelectCapabilities(decoded.Capabilities, selector)
			require.NoError(t, err)
			effective := *decoded
			effective.Capabilities = selection.Capabilities
			fromSet, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
			require.NoError(t, err)
			want, err := LifecycleOf(merged.Capabilities)
			require.NoError(t, err)
			got, err := LifecycleOf(fromSet.Capabilities)
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Len(t, got.Startup, 2+map[bool]int{true: 1}[accept], "identical hooks are not deduplicated")
		})
	}
}

func TestPublishPreservesConditionalContext(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"content": "always"}},
		{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, {Type: CapabilityAgentContext, Config: map[string]any{"contentFile": "/kit/context.md"}}}}},
	}}
	result, err := Merge([]Contribution{{Reference: "source", Descriptor: d}}, MergeOptions{ContextPath: "/set/context.md"})
	require.NoError(t, err)
	require.Len(t, result.ContextSources, 2)
	require.NotEqual(t, result.ContextSources[0].Target, result.ContextSources[1].Target)
	selection, err := SelectCapabilities(result.Descriptor.Capabilities, Supported(CapabilityAgentContext))
	require.NoError(t, err)
	require.Len(t, selection.Capabilities, 1)
	require.Equal(t, "/set/context.md.parts/0.md", selection.Capabilities[0].Config["contentFile"])
	require.Equal(t, "capabilities[0]", selection.Capabilities[0].Source.Path)
	require.Equal(t, "/kit/context.md", d.Capabilities[1].Group.Capabilities[1].Config["contentFile"])
}

func TestGroupConflictNamesOriginalSources(t *testing.T) {
	file := Capability{Type: CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "/same", "content": "x"}}}}
	d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{file, {Group: &CapabilityGroup{Capabilities: []Capability{file}}}}}
	selected, err := SelectCapabilities(d.Capabilities, Supported(CapabilityLifecycle))
	require.NoError(t, err)
	d.Capabilities = selected.Capabilities
	_, err = Compose([]Contribution{{Reference: "source", Descriptor: d}})
	require.ErrorContains(t, err, "capabilities[0]")
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[0]")
	require.ErrorContains(t, err, "source")
}

func TestSelectedContextBodiesRemainSeparate(t *testing.T) {
	items := []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"content": "first"}},
		{Group: &CapabilityGroup{Capabilities: []Capability{{Type: CapabilityAgentContext, Config: map[string]any{"content": "second"}}}}},
	}
	_, err := AgentContextsOf(items)
	require.Error(t, err)
	selected, err := SelectCapabilities(items, Supported(CapabilityAgentContext))
	require.NoError(t, err)
	bodies, err := AgentContextsOf(selected.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []AgentContext{{Content: "first"}, {Content: "second"}}, bodies)
}
