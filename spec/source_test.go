package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCapabilitySourcesRejectExplicitNull(t *testing.T) {
	for _, item := range []string{
		`{"type":"com.example/feature@1","source":null}`,
		`{"source":null,"group":{"capabilities":[{"type":"com.example/feature@1"}]}}`,
		`{"group":{"capabilities":[{"type":"com.example/feature@1","source":null}]}}`,
	} {
		t.Run(item, func(t *testing.T) {
			var decoded Capability
			require.ErrorContains(t, json.Unmarshal([]byte(item), &decoded), "source must be an object")
			require.ErrorContains(t, yaml.Unmarshal([]byte(item), &decoded), "source must be an object")
			var value any
			require.NoError(t, json.Unmarshal([]byte(item), &value))
			raw, err := yaml.Marshal(value)
			require.NoError(t, err)
			require.ErrorContains(t, yaml.Unmarshal(raw, &decoded), "source must be an object")
		})
	}
}
