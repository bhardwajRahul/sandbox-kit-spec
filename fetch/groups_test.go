package fetch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestResolveGroupsThroughPublicAPIs(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			reg := newRegistry(t)
			kind := spec.KindWorkload
			if partial {
				kind = spec.KindMixin
			}
			defaultValue := "9000"
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind, Args: map[string]spec.Arg{"port": {Default: &defaultValue, Env: "PORT"}}, Capabilities: []spec.Capability{
				{Type: spec.CapabilityPort, Config: map[string]any{"container": 8080}},
				{Group: &spec.CapabilityGroup{Name: "same name", Optional: true, Capabilities: []spec.Capability{
					{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}},
					{Type: spec.CapabilityAgentContext, Config: map[string]any{"content": "must not reach a handler"}},
				}}},
			}}
			reg.tag("kits/groups", "1.0.0", reg.image(t, kitJSON(t, d)))
			client, err := New()
			require.NoError(t, err)
			method := client.Resolve
			if partial {
				method = client.ResolvePartial
			}
			calls := 0
			result, err := method(t.Context(), reqs(reg.ref("kits/groups", "1.0.0")), WithCapabilitySelector(func(c spec.Capability) bool {
				calls++
				if c.Type != spec.CapabilityPort {
					return true
				}
				var port spec.Port
				require.NoError(t, spec.DecodeCapabilityConfig(c, &port))
				c.Config["container"] = 1
				return port.Container == 8080
			}))
			require.NoError(t, err)
			require.Equal(t, 3, calls)
			require.Len(t, result.Kits, 1)
			require.Len(t, result.Kits[0].Descriptor.Capabilities, 1)
			require.Len(t, result.Selections[0].Selection.Skipped, 1)
			require.True(t, spec.HasGroups(result.Selections[0].Original.Capabilities))
			require.Equal(t, "9000", result.ContainerEnv["PORT"])
			require.Empty(t, spec.SurfaceOf(result.Descriptor).Services)
			require.Equal(t, []string{"8080/tcp"}, spec.SurfaceOf(result.Descriptor).Ports)
			result.Kits[0].Descriptor.Capabilities[0].Config["container"] = 1234
			var originalPort spec.Port
			require.NoError(t, spec.DecodeCapabilityConfig(result.Selections[0].Original.Capabilities[0], &originalPort))
			require.Equal(t, 8080, originalPort.Container)
		})
	}
}

func TestSelectionPrecedesCredentialOwnershipAndCrossChecks(t *testing.T) {
	reg := newRegistry(t)
	cred := spec.Capability{Type: spec.CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "GH_TOKEN", "inject": []any{map[string]any{"domain": "api.github.com", "header": "Authorization", "format": "Bearer %s"}}}}}
	group := spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
		{Type: "com.example/unavailable@1"}, cred,
	}}}
	base := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Capabilities: []spec.Capability{cred, {Type: spec.CapabilityNetworkPolicy, Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com"}}}}}}
	mixin := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{group}}
	reg.tag("kits/base", "1.0.0", reg.image(t, kitJSON(t, base)))
	reg.tag("kits/group", "1.0.0", reg.image(t, kitJSON(t, mixin)))
	client, err := New()
	require.NoError(t, err)
	requests := reqs(reg.ref("kits/base", "1.0.0"), reg.ref("kits/group", "1.0.0"))
	result, err := client.Resolve(t.Context(), requests)
	require.NoError(t, err)
	require.Len(t, result.Kits[1].Descriptor.Capabilities, 0)
	_, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(func(spec.Capability) bool { return true }))
	require.ErrorContains(t, err, "one credential has one owner")
	require.ErrorContains(t, err, "group.capabilities[1]")
	_, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(func(c spec.Capability) bool { return c.Type != spec.CapabilityNetworkPolicy }))
	require.ErrorContains(t, err, "required capability selection rejected")
	// Make the only policy optional: rejection must now fail final coherence,
	// rather than silently leave a credential injectable outside its allowlist.
	base.Capabilities[1].Optional = true
	reg.tag("kits/base", "2.0.0", reg.image(t, kitJSON(t, base)))
	_, err = client.Resolve(t.Context(), reqs(reg.ref("kits/base", "2.0.0")), WithCapabilitySelector(func(c spec.Capability) bool { return c.Type != spec.CapabilityNetworkPolicy }))
	require.ErrorContains(t, err, "not in the network policy")
}

func TestResolveValidatesSkippedMembersAfterExpansion(t *testing.T) {
	reg := newRegistry(t)
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Args: map[string]spec.Arg{"port": {Required: true}}, Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}}}}}}}
	reg.tag("kits/group", "1.0.0", reg.image(t, kitJSON(t, d)))
	client, err := New()
	require.NoError(t, err)
	_, err = client.ResolvePartial(t.Context(), []Request{{Reference: reg.ref("kits/group", "1.0.0"), Args: map[string]string{"port": "70000"}}}, WithCapabilitySelector(func(spec.Capability) bool { t.Fatal("invalid member reached selection"); return false }))
	require.ErrorContains(t, err, "capabilities[0].group.capabilities[0]")
}

func TestResolveCompletesSelectionSourcesWithoutMutatingDeclarations(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, accept := range []bool{false, true} {
			for _, sourceKit := range []string{"", "original-publisher"} {
				t.Run(fmt.Sprintf("group=%t/accept=%t/source=%s", grouped, accept, sourceKit), func(t *testing.T) {
					reg := newRegistry(t)
					entry := spec.Capability{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}}
					if grouped {
						entry = spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{entry}}}
					} else {
						entry.Optional = true
					}
					entry.Source = &spec.CapabilitySource{Kit: sourceKit, Path: "capabilities[7]"}
					d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{entry}}
					reg.tag("kits/source", "1.0.0", reg.image(t, kitJSON(t, d)))
					client, err := New()
					require.NoError(t, err)
					ref := reg.ref("kits/source", "1.0.0")
					result, err := client.ResolvePartial(t.Context(), reqs(ref), WithCapabilitySelector(func(spec.Capability) bool { return accept }))
					require.NoError(t, err)
					selection := result.Selections[0]
					records := selection.Selection.Skipped
					if accept {
						records = selection.Selection.Selected
					}
					require.Len(t, records, 1)
					wantKit := sourceKit
					if wantKit == "" {
						wantKit = ref
					}
					require.Equal(t, &spec.CapabilitySource{Kit: wantKit, Path: "capabilities[7]"}, records[0].Source)
					original := selection.Original.Capabilities[0].Source
					require.Equal(t, entry.Source, original)
					require.NotSame(t, original, records[0].Source)
				})
			}
		}
	}
}
