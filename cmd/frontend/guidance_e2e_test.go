package main

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

func TestGuidanceBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run the guidance build (needs Docker)")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}

	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	frontend := e.buildFrontend()
	registry := e.startRegistry()

	for _, tt := range []struct {
		name       string
		descriptor string
		file       string
		args       []string
	}{
		{
			name:       "literal",
			descriptor: strings.ReplaceAll(guidanceDescriptor, "${{ kit.args.file }}", "notes-en.md"),
			file:       "notes-en.md",
		},
		{name: "default", descriptor: guidanceDescriptor, file: "notes-en.md"},
		{name: "override", descriptor: guidanceDescriptor, file: "notes-fr.md", args: []string{"--build-arg", "file=notes-fr.md"}},
		{name: "nested", descriptor: guidanceDescriptor, file: "fr/notes.md", args: []string{"--build-arg", "file=fr/notes.md"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &e2e{t: t, builder: e.builder}
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte("# syntax="+frontend+"\n"+tt.descriptor), 0o644))
			for _, file := range []string{"notes-en.md", "notes-fr.md", "fr/notes.md"} {
				name := filepath.Join(dir, "docs", file)
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
				require.NoError(t, os.WriteFile(name, []byte("Guidance from "+file+".\n"), 0o644))
			}

			ref := registry + "/guidance:" + tt.name
			args := append([]string{"buildx", "--builder", e.builder, "build", dir,
				"-f", filepath.Join(dir, "demo.yaml"), "--push", "-t", ref,
				"--platform=linux/amd64,linux/arm64", "--provenance=false"}, tt.args...)
			e.run("docker", args...)

			artifacts, err := tckkit.FromRegistryAll(t.Context(), ref)
			require.NoError(t, err)
			require.Len(t, artifacts, 2)
			for _, artifact := range artifacts {
				d, err := spec.Decode([]byte(artifact.Annotations()[spec.AnnotationDescriptor]))
				require.NoError(t, err)
				ac, err := spec.AgentContextOf(d.Capabilities)
				require.NoError(t, err)
				require.NotNil(t, ac)
				require.Equal(t, "/usr/share/sandbox/kit/demo/"+path.Base(tt.file), ac.ContentFile)

				body, present, err := artifact.ReadFile(t.Context(), ac.ContentFile)
				require.NoError(t, err)
				require.True(t, present)
				require.Equal(t, "Guidance from "+tt.file+".\n", string(body))

				staged, present, err := artifact.ReadFile(t.Context(), "/usr/share/sandbox/kit/demo/kit.yaml")
				require.NoError(t, err)
				require.True(t, present)
				stagedDescriptor, err := spec.Decode(staged)
				require.NoError(t, err)
				require.Equal(t, d, stagedDescriptor)

				report, err := tckkit.Run(t.Context(), artifact)
				require.NoError(t, err)
				require.NoError(t, report.Err(), "%s", report)
			}
		})
	}
}
