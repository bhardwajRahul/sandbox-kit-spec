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
					func(o *resolveOptions) { o.environment = map[string]string{"HOME": "/private-environment-value"} })
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
