package spec

import (
	"encoding/json"
	"fmt"
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

func TestCapabilitySourceFieldTypes(t *testing.T) {
	for _, field := range []string{"kit", "path"} {
		for _, value := range []any{nil, 12, true, []any{}, map[string]any{}} {
			t.Run(fmt.Sprintf("%s=%v", field, value), func(t *testing.T) {
				source := map[string]any{"kit": "original", "path": "capabilities[0]"}
				source[field] = value
				item := map[string]any{"type": "com.example/feature@1", "source": source}
				rawJSON, err := json.Marshal(item)
				require.NoError(t, err)
				rawYAML, err := yaml.Marshal(item)
				require.NoError(t, err)
				var c Capability
				require.Error(t, json.Unmarshal(rawJSON, &c))
				require.Error(t, yaml.Unmarshal(rawYAML, &c))
			})
		}
	}
	for _, source := range []string{`{"path":"capabilities[0]"}`, `{"kit":"original","path":"capabilities[0]"}`} {
		var c CapabilitySource
		require.NoError(t, json.Unmarshal([]byte(source), &c))
		require.Equal(t, "capabilities[0]", c.Path)
		require.NoError(t, yaml.Unmarshal([]byte(source), &c))
	}
	var c CapabilitySource
	require.Error(t, json.Unmarshal([]byte(`{"path":"capabilities[0]","unknown":true}`), &c))
	_, err := Decode([]byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - type: com.example/feature@1\n    source:\n      path: capabilities[0]\n      unknown: true\n"))
	require.Error(t, err)
}
