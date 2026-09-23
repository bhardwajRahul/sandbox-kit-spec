package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/internal/version"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

func TestDescriptorRendersAsBlockYAMLInPublishedOrder(t *testing.T) {
	var out bytes.Buffer
	published := `{"schemaVersion":"3","kind":"workload","displayName":"Demo","version":"true","build":"FROM scratch\nCOPY . /\n","provides":["demo@1.0.0"]}`

	require.NoError(t, writeDescriptorYAML(&out, []byte(published)))
	require.Equal(t, `schemaVersion: "3"
kind: workload
displayName: Demo
version: "true"
build: |
  FROM scratch
  COPY . /
provides:
  - demo@1.0.0
`, out.String())
}

func demoInspection(recipe string) *tckkit.Inspection {
	in := &tckkit.Inspection{
		Descriptor: []byte(`{"schemaVersion":"3","kind":"workload"}`),
		Stem:       "demo",
	}
	if recipe != "" {
		in.Recipe = []byte(recipe)
	}
	return in
}

// Platforms whose sources agree read as one group, the way identical kit
// reports do; the text wears the same header a validate run does.
func TestInspectTextGroupsPlatformsLikeAValidateRun(t *testing.T) {
	o := inspection{target: "docker.io/me/demo:1"}
	o.add("linux/amd64", demoInspection("FROM scratch\n\nCOPY . /\n"))
	o.add("linux/arm64", demoInspection("FROM scratch\n\nCOPY . /\n"))

	var out bytes.Buffer
	p := &presentation{color: "never"}
	require.NoError(t, p.writeInspectionText(&out, o))
	require.Equal(t, "kit-tck "+version.String()+` · inspect · docker.io/me/demo:1

linux/amd64, linux/arm64
  descriptor  /usr/share/sandbox/kit/demo/kit.yaml
    schemaVersion: "3"
    kind: workload

  dockerfile  /usr/share/sandbox/kit/demo/kit.dockerfile
    FROM scratch

    COPY . /

`, out.String())
}

func TestInspectJSONSharesTheKitEnvelope(t *testing.T) {
	o := inspection{target: "docker.io/me/demo:1"}
	o.add("linux/amd64", demoInspection("FROM scratch\n"))
	o.add("linux/arm64", demoInspection(""))

	var out bytes.Buffer
	require.NoError(t, writeInspectionJSON(&out, o, halves{descriptor: true, recipe: true}))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "kit-tck", got["tool"])
	require.Equal(t, "inspect", got["suite"])
	require.Equal(t, "docker.io/me/demo:1", got["target"])

	results := got["results"].([]any)
	require.Len(t, results, 2, "images that disagree are reported apart")
	first := results[0].(map[string]any)
	require.Equal(t, "FROM scratch\n", first["dockerfile"])
	require.Equal(t, "workload", first["descriptor"].(map[string]any)["kind"])
	second := results[1].(map[string]any)
	require.Contains(t, second, "dockerfile")
	require.Nil(t, second["dockerfile"], "a kit with no recipe says so, rather than leaving the field out")

	_, err := o.single("linux/s390x")
	require.ErrorContains(t, err, "--platform")
}

// A workload's derived provides differ by architecture, so raw output
// reads the host's image rather than refusing to print anything.
func TestRawOutputReadsTheHostsImageWhenPlatformsDisagree(t *testing.T) {
	o := inspection{target: "docker.io/me/demo:1"}
	o.add("linux/amd64", demoInspection("FROM amd64\n"))
	o.add("linux/arm64/v8", demoInspection("FROM arm64\n"))

	in, err := o.single("linux/arm64")
	require.NoError(t, err)
	require.Equal(t, "FROM arm64\n", string(in.Recipe),
		"the host knows its architecture, not the variant an index labelled")

	in, err = o.single("linux/amd64")
	require.NoError(t, err)
	require.Equal(t, "FROM amd64\n", string(in.Recipe))
}

func TestRawOutputOfAgreeingPlatformsNeedsNoHostImage(t *testing.T) {
	o := inspection{target: "docker.io/me/demo:1"}
	o.add("linux/amd64", demoInspection("FROM scratch\n"))
	o.add("linux/arm64", demoInspection("FROM scratch\n"))

	in, err := o.single("linux/s390x")
	require.NoError(t, err)
	require.Equal(t, "FROM scratch\n", string(in.Recipe))
}
