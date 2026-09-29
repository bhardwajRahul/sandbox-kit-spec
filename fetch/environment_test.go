package fetch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestResolveEnvironmentDiagnosticsUseDecodedReferences(t *testing.T) {
	for _, form := range []struct{ name, args, path string }{
		{"escaped space", "", `"${{\u0020kit.env.HOME}}/config"`},
		{"escaped dollar", "", `"\u0024{{ kit.env.HOME }}/config"`},
		{"escaped newline", "", `"${{\nkit.env.HOME}}/config"`},
		{"argument fragments", `args:
  left: {default: '${{'}
  right: {default: ' kit.env.HOME }}'}
`, `'${{kit.args.left}}${{kit.args.right}}/config'`},
	} {
		for _, failure := range []string{"composition", "final validation"} {
			t.Run(form.name+"/"+failure, func(t *testing.T) {
				var kits []*Kit
				for i := range 2 {
					filePath, content := form.path, "data"
					if failure == "final validation" {
						filePath = strings.ReplaceAll(filePath, "/config", fmt.Sprintf("/config-%d", i))
						content = strings.Repeat("x", spec.SizeErrorBytes/2+1024)
					}
					raw := []byte("schemaVersion: \"3\"\nkind: mixin\n" + form.args + `capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: ` + filePath + `
          content: ` + content + "\n")
					require.False(t, spec.ContainsEnvRef(string(raw)), "regression requires the serialized scan to miss the reference")
					d, err := spec.Decode(raw)
					require.NoError(t, err)
					kits = append(kits, &Kit{Reference: fmt.Sprintf("example.com/kit%d:1.0.0", i), Digest: digest.FromBytes(raw).String(), Raw: raw, Descriptor: d})
				}
				result, err := mergeKits(kits, []map[string]string{nil, nil}, true,
					WithEnvironment(map[string]string{"HOME": "/private-environment-value"}, nil))
				require.Nil(t, result)
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-environment-value")
				if failure == "composition" {
					require.ErrorContains(t, err, "compose capabilities after environment expansion")
				} else {
					require.ErrorContains(t, err, "merged descriptor is invalid after environment expansion")
				}
			})
		}
	}
}

func TestResolveWithEnvironment(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			reg := newRegistry(t)
			kind := spec.KindWorkload
			if partial {
				kind = spec.KindMixin
			}
			home := "/home/export"
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind,
				Args: map[string]spec.Arg{"home": {Default: &home, Env: "HOME"}},
				Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
					"files": []any{map[string]any{"path": "${{kit.env.HOME}}/config", "content": "${{kit.env.MESSAGE}}"}},
				}}},
			}
			reg.tag("kits/environment", "1.0.0", reg.image(t, kitJSON(t, d)))
			client, err := New()
			require.NoError(t, err)
			resolve := client.Resolve
			if partial {
				resolve = client.ResolvePartial
			}
			requests := reqs(reg.ref("kits/environment", "1.0.0"))
			t.Setenv("MESSAGE", "host-value-must-not-be-used")
			_, err = resolve(t.Context(), requests)
			require.ErrorContains(t, err, `variable "MESSAGE"`)

			for _, tc := range []struct {
				name, message, wantHome string
				overrides               map[string]string
			}{
				{"exports replace defaults", "image-message", home, nil},
				{"overrides replace exports", "caller-message", "/home/caller", map[string]string{"HOME": "/home/caller", "MESSAGE": "caller-message"}},
				{"empty override is present", "", home, map[string]string{"MESSAGE": ""}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					defaults := map[string]string{"HOME": "/home/image", "MESSAGE": "image-message"}
					option := WithEnvironment(defaults, tc.overrides)
					defaults["HOME"], defaults["MESSAGE"] = "/mutated", "mutated"
					if tc.overrides != nil {
						tc.overrides["MESSAGE"] = "mutated"
					}
					calls := 0
					for range 2 { // Reusing an option cannot retain mutations from a previous result.
						result, err := resolve(t.Context(), requests, option, WithCapabilitySelector(func(c spec.Capability) bool {
							lc, err := spec.LifecycleOf([]spec.Capability{c})
							require.NoError(t, err)
							require.Equal(t, tc.wantHome+"/config", lc.Files[0].Path)
							require.Equal(t, tc.message, lc.Files[0].Content)
							calls++
							return true
						}))
						require.NoError(t, err)
						require.Equal(t, map[string]string{"HOME": home}, result.ContainerEnv)
						lc, err := spec.LifecycleOf(result.Descriptor.Capabilities)
						require.NoError(t, err)
						require.Equal(t, tc.wantHome+"/config", lc.Files[0].Path)
						require.Equal(t, tc.message, lc.Files[0].Content)
						result.ContainerEnv["HOME"] = "/mutated-result"
					}
					require.Equal(t, 2, calls)
				})
			}
		})
	}
}
