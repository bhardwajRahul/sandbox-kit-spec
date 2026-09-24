package assemble

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/klauspost/compress/zstd"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func layerDesc(seed string) (ocispec.Descriptor, godigest.Digest) {
	blob := godigest.FromString(seed)
	diffID := godigest.FromString(seed + "-uncompressed")
	return ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayerGzip,
		Digest:    blob,
		Size:      int64(len(seed)),
	}, diffID
}

func input(name string, layerSeeds ...string) Input {
	in := Input{Name: name}
	in.Manifest.SchemaVersion = 2
	in.Manifest.MediaType = ocispec.MediaTypeImageManifest
	for _, seed := range layerSeeds {
		desc, diffID := layerDesc(seed)
		in.Manifest.Layers = append(in.Manifest.Layers, desc)
		in.Config.RootFS.DiffIDs = append(in.Config.RootFS.DiffIDs, diffID)
	}
	in.Config.RootFS.Type = "layers"
	return in
}

func TestMerge(t *testing.T) {
	sandbox := input("claude-kit", "base-rootfs", "claude-layer")
	sandbox.Config.Config.Entrypoint = []string{"/usr/local/bin/claude"}
	sandbox.Config.Config.Env = []string{"PATH=/usr/local/bin:/usr/bin"}

	gh := input("gh-kit", "gh-overlay")
	node := input("node-kit", "node-a", "node-b")

	merged, err := Merge(sandbox, []Input{gh, node})
	require.NoError(t, err)

	require.Len(t, merged.Manifest.Layers, 5, "sandbox layers then mixin layers, in order")
	require.Equal(t, sandbox.Manifest.Layers[0].Digest, merged.Manifest.Layers[0].Digest)
	require.Equal(t, gh.Manifest.Layers[0].Digest, merged.Manifest.Layers[2].Digest)
	require.Equal(t, node.Manifest.Layers[1].Digest, merged.Manifest.Layers[4].Digest)

	require.Len(t, merged.Config.RootFS.DiffIDs, 5, "diff_ids concatenate in the same order")
	require.Equal(t, gh.Config.RootFS.DiffIDs[0], merged.Config.RootFS.DiffIDs[2])

	require.Equal(t, []string{"/usr/local/bin/claude"}, merged.Config.Config.Entrypoint,
		"runtime contract travels unchanged from the sandbox")

	require.Equal(t, godigest.FromBytes(merged.ConfigJSON), merged.Manifest.Config.Digest)
	require.Equal(t, int64(len(merged.ConfigJSON)), merged.Manifest.Config.Size)

	require.Len(t, merged.Config.History, 3, "one synthesized history entry per mixin layer")
	require.Contains(t, merged.Config.History[0].CreatedBy, "gh-kit")
}

// Assembled images are addressed by their lock, and sandboxes sharing a
// lock are meant to share one image record — which holds only if merging
// the same inputs produces the same bytes. A wall-clock timestamp in the
// synthesized history would give every assembly a different digest.
func TestMergeIsDeterministic(t *testing.T) {
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config.Entrypoint = []string{"bash"}
	mixin := input("tool-kit", "tool-overlay")
	mixin.Config.Config.Env = []string{"PATH=/opt/tool/bin"}

	first, err := Merge(sandbox, []Input{mixin})
	require.NoError(t, err)
	second, err := Merge(sandbox, []Input{mixin})
	require.NoError(t, err)

	require.Equal(t, string(first.ConfigJSON), string(second.ConfigJSON))
	require.Equal(t, first.Manifest.Config.Digest, second.Manifest.Config.Digest,
		"one lock must name one image, so the same inputs must merge byte-identically")
	for _, h := range first.Config.History {
		require.Nil(t, h.Created, "a synthesized history entry carries no clock")
	}
}

func TestMergeConfigAdditive(t *testing.T) {
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config.Entrypoint = []string{"bash"}
	sandbox.Config.Config.Env = []string{"PATH=/usr/local/bin:/usr/bin", "HOME=/home/agent"}
	sandbox.Config.Config.Labels = map[string]string{"vendor": "docker"}

	tool := input("tool-kit", "tool-overlay")
	tool.Config.Config.Env = []string{"PATH=/opt/tool/bin", "TOOL_HOME=/opt/tool"}
	tool.Config.Config.Labels = map[string]string{"tool": "1"}
	tool.Config.Config.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	tool.Config.Config.Volumes = map[string]struct{}{"/var/cache/tool": {}}

	agent := input("agent-kit", "agent-overlay")
	agent.Config.Config.Env = []string{"PATH=/opt/agent/bin:/usr/bin", "HOME=/home/agent"}
	agent.Config.Config.ExposedPorts = map[string]struct{}{"9090/tcp": {}}
	agent.Config.Config.Entrypoint = []string{"/opt/agent/bin/agent"}
	agent.Config.Config.Cmd = []string{"--serve"}
	agent.Config.Config.User = "agent"
	agent.Config.Config.WorkingDir = "/opt/agent"

	merged, err := Merge(sandbox, []Input{tool, agent})
	require.NoError(t, err)

	require.Equal(t, []string{
		"PATH=/usr/local/bin:/usr/bin:/opt/tool/bin:/opt/agent/bin",
		"HOME=/home/agent",
		"TOOL_HOME=/opt/tool",
	}, merged.Config.Config.Env,
		"PATH appends missing elements in composition order; equal restatements are fine; new vars append")
	require.Equal(t, map[string]string{"vendor": "docker", "tool": "1"}, merged.Config.Config.Labels)
	require.Equal(t, map[string]struct{}{"8080/tcp": {}, "9090/tcp": {}}, merged.Config.Config.ExposedPorts)
	require.Equal(t, map[string]struct{}{"/var/cache/tool": {}}, merged.Config.Config.Volumes)
	require.Equal(t, []string{"bash"}, merged.Config.Config.Entrypoint,
		"a mixin's entrypoint is standalone-run surface, never merged")
	require.Empty(t, merged.Config.Config.Cmd)
	require.Empty(t, merged.Config.Config.User)
	require.Empty(t, merged.Config.Config.WorkingDir)
}

func TestMergePreservesSandboxConfig(t *testing.T) {
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config = ocispec.ImageConfig{
		Env:          []string{"PATH=/usr/bin"},
		Labels:       map[string]string{"vendor": "docker"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}
	original, err := Merge(sandbox, nil)
	require.NoError(t, err)

	tool := input("tool-kit", "tool-overlay")
	tool.Config.Config = ocispec.ImageConfig{
		Env:          []string{"PATH=/opt/tool/bin"},
		Labels:       map[string]string{"tool": "1"},
		ExposedPorts: map[string]struct{}{"8080/tcp": {}},
		Volumes:      map[string]struct{}{"/var/cache/tool": {}},
	}

	merged, err := Merge(sandbox, []Input{tool})
	require.NoError(t, err)
	require.Equal(t, []string{"PATH=/usr/bin:/opt/tool/bin"}, merged.Config.Config.Env)
	require.Equal(t, "1", merged.Config.Config.Labels["tool"])
	require.Contains(t, merged.Config.Config.ExposedPorts, "8080/tcp")
	require.Contains(t, merged.Config.Config.Volumes, "/var/cache/tool")
	require.Equal(t, ocispec.ImageConfig{
		Env:          []string{"PATH=/usr/bin"},
		Labels:       map[string]string{"vendor": "docker"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}, sandbox.Config.Config)

	withoutTool, err := Merge(sandbox, nil)
	require.NoError(t, err)
	require.Equal(t, original.ConfigJSON, withoutTool.ConfigJSON)
}

func TestMergePreservesSandboxConfigOnError(t *testing.T) {
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config = ocispec.ImageConfig{
		Env:          []string{"PATH=/usr/bin", "EDITOR=vim"},
		Labels:       map[string]string{"vendor": "docker"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}
	tool := input("tool-kit", "tool-overlay")
	tool.Config.Config = ocispec.ImageConfig{
		Env:          []string{"PATH=/opt/tool/bin"},
		Labels:       map[string]string{"tool": "1"},
		ExposedPorts: map[string]struct{}{"8080/tcp": {}},
		Volumes:      map[string]struct{}{"/var/cache/tool": {}},
	}
	conflicting := input("editor-kit", "editor-overlay")
	conflicting.Config.Config.Env = []string{"EDITOR=nano"}

	_, err := Merge(sandbox, []Input{tool, conflicting})
	require.ErrorContains(t, err, "env conflict on EDITOR")
	require.Equal(t, ocispec.ImageConfig{
		Env:          []string{"PATH=/usr/bin", "EDITOR=vim"},
		Labels:       map[string]string{"vendor": "docker"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}, sandbox.Config.Config)
}

func TestMergeConfigPathOnSandboxWithoutPath(t *testing.T) {
	sandbox := input("bare-kit", "rootfs")
	mixin := input("tool-kit", "overlay")
	mixin.Config.Config.Env = []string{"PATH=/opt/tool/bin"}

	merged, err := Merge(sandbox, []Input{mixin})
	require.NoError(t, err)
	require.Equal(t, []string{"PATH=/opt/tool/bin"}, merged.Config.Config.Env)
}

func TestMergeConfigEnvConflict(t *testing.T) {
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config.Env = []string{"EDITOR=vim"}
	mixin := input("tool-kit", "overlay")
	mixin.Config.Config.Env = []string{"EDITOR=nano"}

	_, err := Merge(sandbox, []Input{mixin})
	require.ErrorContains(t, err, "env conflict on EDITOR")
	require.ErrorContains(t, err, "shell-kit")
	require.ErrorContains(t, err, "tool-kit")
}

func TestMergeConfigLabelDisagreementKeepsFirstWriter(t *testing.T) {
	// buildx stamps its own recipe path into every image it builds, so
	// kits disagree on this label by construction; failing the
	// composition over inert metadata would make any two kits
	// uncomposable.
	sandbox := input("shell-kit", "rootfs")
	sandbox.Config.Config.Labels = map[string]string{
		"com.docker.image.source.entrypoint": "examples/shell/shell.yaml",
	}
	a := input("a-kit", "a-overlay")
	a.Config.Config.Labels = map[string]string{
		"com.docker.image.source.entrypoint": "examples/claude-acp/claude-acp.yaml",
		"team":                               "one",
	}
	b := input("b-kit", "b-overlay")
	b.Config.Config.Labels = map[string]string{"team": "two"}

	merged, err := Merge(sandbox, []Input{a, b})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"com.docker.image.source.entrypoint": "examples/shell/shell.yaml",
		"team":                               "one",
	}, merged.Config.Config.Labels)
}

func TestMergeRejectsLayerDiffIDMismatch(t *testing.T) {
	sandbox := input("claude-kit", "base")
	broken := input("broken-kit", "layer")
	broken.Config.RootFS.DiffIDs = nil

	_, err := Merge(sandbox, []Input{broken})
	require.ErrorContains(t, err, "broken-kit")
	require.ErrorContains(t, err, "diff_ids")
}

func TestTagIsDeterministic(t *testing.T) {
	a := Tag([]byte(`{"version":1}`))
	require.Equal(t, a, Tag([]byte(`{"version":1}`)))
	require.NotEqual(t, a, Tag([]byte(`{"version":2}`)))
	require.Contains(t, a, "sandbox-kit-assembled:")
}

func tarLayer(t *testing.T, gzipped bool, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w *tar.Writer
	var gz *gzip.Writer
	if gzipped {
		gz = gzip.NewWriter(&buf)
		w = tar.NewWriter(gz)
	} else {
		w = tar.NewWriter(&buf)
	}
	require.NoError(t, w.WriteHeader(&tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}))
	for name, content := range entries {
		require.NoError(t, w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}))
		_, err := w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	if gz != nil {
		require.NoError(t, gz.Close())
	}
	return buf.Bytes()
}

func TestReadInventory(t *testing.T) {
	for _, gzipped := range []bool{true, false} {
		blob := tarLayer(t, gzipped, map[string]string{"usr/local/bin/tool": "#!/bin/sh"})
		files, err := ReadInventory(bytes.NewReader(blob))
		require.NoError(t, err)
		require.Equal(t, []string{"usr/local/bin/tool"}, files, "directories excluded, gzipped=%v", gzipped)
	}

	// tar+zstd: registries serve zstd-compressed kit layers when the
	// builder (or a base image's publisher) chose zstd — the shell
	// template's layers arrive this way.
	plain := tarLayer(t, false, map[string]string{"usr/local/bin/tool": "#!/bin/sh"})
	var zbuf bytes.Buffer
	zw, err := zstd.NewWriter(&zbuf)
	require.NoError(t, err)
	_, err = zw.Write(plain)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	files, err := ReadInventory(bytes.NewReader(zbuf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, []string{"usr/local/bin/tool"}, files, "zstd layers inventory like gzip ones")
}

func TestCheckCollisions(t *testing.T) {
	require.NoError(t, CheckCollisions([]Inventory{
		{Kit: "gh-kit", Files: []string{"usr/local/bin/gh"}},
		{Kit: "node-kit", Files: []string{"usr/local/bin/node"}},
	}), "distinct files in a shared directory compose")

	require.NoError(t, CheckCollisions([]Inventory{
		{Kit: "node-kit", Files: []string{"usr/local/bin/node", "usr/local/bin/node"}},
	}), "a kit's own layers may repeat a path")

	err := CheckCollisions([]Inventory{
		{Kit: "gh-kit", Files: []string{"usr/local/bin/tool"}},
		{Kit: "other-kit", Files: []string{"usr/local/bin/tool"}},
	})
	require.ErrorContains(t, err, "/usr/local/bin/tool")
	require.ErrorContains(t, err, "gh-kit and other-kit")
}

// Applying an OCI layer keeps the last entry when a path repeats inside
// one tar, so resolution must report the replacement, not the original.
func TestReadFileKeepsTheLastEntry(t *testing.T) {
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, body := range []string{"original", "replacement"} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "usr/share/sandbox/kit/demo/kit.yaml", Mode: 0o644, Size: int64(len(body)),
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	body, ok, err := ReadFile(bytes.NewReader(layer.Bytes()), "/usr/share/sandbox/kit/demo/kit.yaml")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "replacement", string(body))
}
