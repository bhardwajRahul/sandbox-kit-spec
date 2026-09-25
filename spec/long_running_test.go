package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLongRunningValidation(t *testing.T) {
	base := func(caps ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: caps}
	}
	for _, optional := range []bool{false, true} {
		entry := Capability{Type: CapabilityLongRunning, Optional: optional}
		_, err := Validate(base(entry))
		require.NoError(t, err)
		mixin := base(entry)
		mixin.Kind = KindMixin
		_, err = Validate(mixin)
		require.ErrorContains(t, err, "workload-only")
	}

	_, err := Validate(base(
		Capability{Type: CapabilityLongRunning},
		Capability{Type: CapabilityLongRunning, Optional: true},
	))
	require.ErrorContains(t, err, "already declared")

	for _, spelling := range []string{"{}", "null", `{"keepAlive": true}`} {
		t.Run(spelling, func(t *testing.T) {
			var fromYAML, fromJSON Capability
			require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityLongRunning+"\nconfig: "+spelling+"\n"), &fromYAML))
			require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityLongRunning+`","config":`+spelling+`}`), &fromJSON))
			for _, entry := range []Capability{fromYAML, fromJSON} {
				_, err := Validate(base(entry))
				require.ErrorContains(t, err, "takes no config")
			}
		})
	}

	set := base(Capability{Type: CapabilityLongRunning})
	set.Kind = KindSet
	set.Kits = []Kit{{Ref: "example.com/workload:1"}}
	_, err = Validate(set)
	require.NoError(t, err, "set kind is checked after resolution")
}

func TestLongRunningMergeAndSurface(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin} {
		base := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind}
		// A set's own declarations enter Merge without a kind; its
		// members supply the resolved kind that validation judges.
		own := &Descriptor{SchemaVersion: SchemaVersion, Capabilities: []Capability{{Type: CapabilityLongRunning}}}
		if kind == KindWorkload {
			base.Capabilities = []Capability{{Type: CapabilityLongRunning, Optional: true}}
		}
		merged := mergeOK(t, Contribution{Reference: "base", Descriptor: base}, Contribution{Reference: "set", Descriptor: own}).Descriptor
		_, err := Validate(merged)
		if kind == KindMixin {
			require.ErrorContains(t, err, "workload-only")
			continue
		}
		require.NoError(t, err)
		require.Len(t, merged.Capabilities, 1)
		require.False(t, merged.Capabilities[0].Optional, "a required declaration wins")
		require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(merged), "lifetime grants no permission surface")
	}
}
