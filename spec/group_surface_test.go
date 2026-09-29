package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupedNetworkSurfaceIsCanonical(t *testing.T) {
	policy := func(hosts ...string) Capability {
		t.Helper()
		var allow, deny []NetworkEntry
		for _, host := range hosts {
			allow = append(allow, NetworkEntry{Hosts: []string{host}},
				NetworkEntry{Hosts: []string{host}, Methods: []string{"GET"}, Paths: []string{"/api"}})
			deny = append(deny, NetworkEntry{Hosts: []string{"deny." + host}},
				NetworkEntry{Hosts: []string{"deny." + host}, Methods: []string{"POST"}, Paths: []string{"/admin"}})
		}
		rules := &NetworkRulesV2{Allow: allow, Deny: deny}
		c, err := CapabilityWithConfig(Capability{Type: CapabilityNetworkPolicyV2}, PhasedNetworkV2{Install: rules, Runtime: rules})
		require.NoError(t, err)
		return *c
	}
	group := func(c Capability) Capability {
		return Capability{Group: &CapabilityGroup{Capabilities: []Capability{c}}}
	}
	want := SurfaceOf(&Descriptor{Capabilities: []Capability{policy("a.example", "z.example")}})
	for _, entries := range [][]Capability{
		{policy("z.example"), group(policy("a.example", "z.example"))},
		{group(policy("a.example", "z.example")), policy("z.example")},
	} {
		d := &Descriptor{Kind: KindMixin, Capabilities: entries}
		_, err := Validate(d)
		require.NoError(t, err)
		got := SurfaceOf(d)
		require.Equal(t, want, got, "every network projection must sort and deduplicate across blocks")
		wantJSON, err := json.Marshal(want)
		require.NoError(t, err)
		gotJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.Equal(t, string(wantJSON), string(gotJSON))
		require.Empty(t, DiffWidenings(want, got))
		require.Empty(t, DiffWidenings(got, want))
	}
}
