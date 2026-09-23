package kit

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specsgo "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/content/oci"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// layoutFixture writes a kit into an OCI layout the way an exporter would,
// so the layout source is exercised against real blobs — a tar layer, a
// config, a manifest — rather than an in-memory stand-in.
func layoutFixture(t *testing.T, stagedName string, body []byte, extraLayer ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: stagedName, Mode: 0o644, Size: int64(len(body)),
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	layers := []ocispec.Descriptor{push(ocispec.MediaTypeImageLayer, layer.Bytes())}
	// A second layer lets a fixture express deletion, which is the only
	// way to test that resolution honours whiteouts.
	if len(extraLayer) > 0 {
		var upper bytes.Buffer
		tw := tar.NewWriter(&upper)
		for _, name := range extraLayer {
			hdr := &tar.Header{Name: name, Mode: 0o644, Size: 0}
			// A trailing slash marks a directory entry, which is how an
			// upper layer replaces a lower file with a directory.
			if strings.HasSuffix(name, "/") {
				hdr.Typeflag = tar.TypeDir
				hdr.Mode = 0o755
			}
			require.NoError(t, tw.WriteHeader(hdr))
		}
		require.NoError(t, tw.Close())
		layers = append(layers, push(ocispec.MediaTypeImageLayer, upper.Bytes()))
	}

	config, err := json.Marshal(ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"},
		Config:   ocispec.ImageConfig{Entrypoint: []string{"bash"}},
	})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)

	d, err := spec.Decode(body)
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	annotations := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: d.SchemaVersion,
	}
	for k, v := range spec.OCIAnnotations(d) {
		annotations[k] = v
	}

	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned:   specsgo.Versioned{SchemaVersion: 2},
		MediaType:   ocispec.MediaTypeImageManifest,
		Config:      configDesc,
		Layers:      layers,
		Annotations: annotations,
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	return dir, "kit"
}

const fixtureDescriptor = "schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n"

// misshapenLayout is layoutFixture with the manifest bent out of the
// spec's shape before it is pushed, so resolution's gate is testable.
func misshapenLayout(t *testing.T, mutate func(m *ocispec.Manifest)) (string, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, layer.Bytes())
	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)

	m := ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	mutate(&m)
	body, err := json.Marshal(m)
	require.NoError(t, err)
	manifestDesc := push(m.MediaType, body)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))
	return dir, "kit"
}

// The spec's recognition rule is a plain image manifest with an
// image-config blob; anything else still decodes as a manifest struct, so
// resolution has to refuse it rather than judge it.
func TestAnArtifactTypedManifestIsNotAKit(t *testing.T) {
	dir, tag := misshapenLayout(t, func(m *ocispec.Manifest) {
		m.ArtifactType = "application/vnd.example.kit"
	})

	_, err := FromLayout(context.Background(), dir, tag)
	require.ErrorContains(t, err, "artifactType")

	_, err = FromLayoutAll(context.Background(), dir, tag)
	require.ErrorContains(t, err, "artifactType")
}

func TestANonImageConfigIsNotAKit(t *testing.T) {
	dir, tag := misshapenLayout(t, func(m *ocispec.Manifest) {
		m.Config.MediaType = "application/vnd.example.config+json"
	})

	_, err := FromLayout(context.Background(), dir, tag)
	require.ErrorContains(t, err, "image config")

	_, err = FromLayoutAll(context.Background(), dir, tag)
	require.ErrorContains(t, err, "image config")
}

// The OCI image spec requires schemaVersion 2 on indexes and manifests;
// a malformed index fronting otherwise valid manifests must be refused,
// not silently descended through.
func TestAnIndexMustDeclareSchemaVersion2(t *testing.T) {
	dir, tag := layoutFixture(t,
		path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName),
		[]byte(fixtureDescriptor))
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()
	manifestDesc, err := store.Resolve(ctx, tag)
	require.NoError(t, err)

	bad, err := json.Marshal(ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{manifestDesc},
	})
	require.NoError(t, err)
	badDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromBytes(bad),
		Size:      int64(len(bad)),
	}
	require.NoError(t, store.Push(ctx, badDesc, bytes.NewReader(bad)))
	require.NoError(t, store.Tag(ctx, badDesc, "bad"))

	_, err = FromLayout(ctx, dir, "bad")
	require.ErrorContains(t, err, "schemaVersion")
	_, err = FromLayoutAll(ctx, dir, "bad")
	require.ErrorContains(t, err, "schemaVersion")
}

func TestAManifestMustDeclareSchemaVersion2(t *testing.T) {
	dir, tag := misshapenLayout(t, func(m *ocispec.Manifest) {
		m.SchemaVersion = 0
	})

	_, err := FromLayout(context.Background(), dir, tag)
	require.ErrorContains(t, err, "schemaVersion")
}

// A layer may stage the descriptor through a link; the composed
// filesystem resolves it, so the reader must too instead of reporting the
// link's empty payload as the file.
func TestAStagedDescriptorBehindASymlinkIsReadable(t *testing.T) {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	body := []byte(fixtureDescriptor)
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "usr/share/sandbox/kit/demo/kit.real", Mode: 0o644, Size: int64(len(body)),
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "usr/share/sandbox/kit/demo/" + stagedDescriptorName,
		Typeflag: tar.TypeSymlink, Linkname: "kit.real",
	}))
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, layer.Bytes())

	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)

	got, present, err := artifact.ReadFile(ctx,
		"/usr/share/sandbox/kit/demo/"+stagedDescriptorName)
	require.NoError(t, err)
	require.True(t, present, "the link resolves to a staged file")
	require.Equal(t, body, got)
}

// A hard link captured its target's content when its layer was applied; a
// later layer replacing the target path must not change what the link
// serves, or a replacement holding expected bytes would pass for the
// staged original.
func TestAHardLinkIsNotRetargetedByALaterLayer(t *testing.T) {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}
	tarLayer := func(build func(tw *tar.Writer)) ocispec.Descriptor {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		build(tw)
		require.NoError(t, tw.Close())
		return push(ocispec.MediaTypeImageLayer, buf.Bytes())
	}

	original := []byte(fixtureDescriptor)
	lower := tarLayer(func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "usr/share/sandbox/kit/demo/kit.real", Mode: 0o644, Size: int64(len(original)),
		}))
		_, err := tw.Write(original)
		require.NoError(t, err)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     "usr/share/sandbox/kit/demo/" + stagedDescriptorName,
			Typeflag: tar.TypeLink, Linkname: "usr/share/sandbox/kit/demo/kit.real",
		}))
	})
	replacement := []byte("schemaVersion: \"3\"\nkind: workload\ndisplayName: Impostor\n")
	upper := tarLayer(func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "usr/share/sandbox/kit/demo/kit.real", Mode: 0o644, Size: int64(len(replacement)),
		}))
		_, err := tw.Write(replacement)
		require.NoError(t, err)
	})

	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{lower, upper},
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)

	got, present, err := artifact.ReadFile(ctx,
		"/usr/share/sandbox/kit/demo/"+stagedDescriptorName)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, original, got,
		"the hard link serves the inode captured at its own layer, not the later replacement")
}

// A directory taking a path in an upper layer replaces the file a lower
// layer put there: the composed filesystem exposes a directory, so the
// resolver must stop serving the file.
func TestADirectoryReplacingTheDescriptorHidesIt(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), staged+"/")

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.False(t, present, "a directory owns the path now; the file is gone")

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems, "the inventory agrees with content resolution")
}

// Within one tar, a hard link captures the inode present when the link
// entry applies; rewriting the target LATER in the same tar replaces the
// path with a new inode and must not retarget the link.
func TestASameLayerHardLinkKeepsItsInode(t *testing.T) {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	original := []byte(fixtureDescriptor)
	replacement := []byte("schemaVersion: \"3\"\nkind: workload\ndisplayName: Impostor\n")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeFile := func(name string, body []byte) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	writeFile("usr/share/sandbox/kit/demo/kit.real", original)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "usr/share/sandbox/kit/demo/" + stagedDescriptorName,
		Typeflag: tar.TypeLink, Linkname: "usr/share/sandbox/kit/demo/kit.real",
	}))
	writeFile("usr/share/sandbox/kit/demo/kit.real", replacement)
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, buf.Bytes())

	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)

	got, present, err := artifact.ReadFile(ctx, "/usr/share/sandbox/kit/demo/"+stagedDescriptorName)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, original, got, "the link holds the inode from before the rewrite")

	got, present, err = artifact.ReadFile(ctx, "/usr/share/sandbox/kit/demo/kit.real")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, replacement, got, "the path itself holds the rewrite")
}

// Replacing an ancestor directory with a regular file hides everything
// beneath it: the composed filesystem cannot expose a path through what
// is no longer a directory.
func TestAFileReplacingAnAncestorHidesTheSubtree(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), "usr/share/sandbox/kit/demo")

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.False(t, present, "the ancestor is a file now; nothing beneath it is reachable")

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems, "the inventory agrees with content resolution")
}

// A stem directory may be a symlink to where the sources really live; the
// composed filesystem exposes the descriptor through it, so the resolver
// must too instead of treating the link as a subtree blocker.
func TestASymlinkedStemDirectoryIsReadable(t *testing.T) {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	body := []byte(fixtureDescriptor)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "opt/real/" + stagedDescriptorName, Mode: 0o644, Size: int64(len(body)),
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "usr/share/sandbox/kit/demo", Typeflag: tar.TypeSymlink, Linkname: "/opt/real",
	}))
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, buf.Bytes())

	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)

	got, present, err := artifact.ReadFile(ctx,
		"/usr/share/sandbox/kit/demo/"+stagedDescriptorName)
	require.NoError(t, err)
	require.True(t, present, "the symlinked ancestor redirects, not blocks")
	require.Equal(t, body, got)

	stems, err := artifact.StagedStems(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, stems, "the symlinked stem is discovered")
}

// The staged root itself may sit behind a symlink; discovery follows it
// the way reads do, or a filesystem-valid kit reads fine and is still
// rejected as staging nothing.
func TestASymlinkedStagedRootIsDiscovered(t *testing.T) {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	body := []byte(fixtureDescriptor)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "opt/kits/demo/" + stagedDescriptorName, Mode: 0o644, Size: int64(len(body)),
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "usr/share/sandbox/kit", Typeflag: tar.TypeSymlink, Linkname: "/opt/kits",
	}))
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, buf.Bytes())

	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))

	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)

	stems, err := artifact.StagedStems(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"demo"}, stems)

	got, present, err := artifact.ReadFile(ctx,
		"/usr/share/sandbox/kit/demo/"+stagedDescriptorName)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, body, got)
}

// buildLayerArtifact stores one layer as an OCI layout and opens it as
// an artifact, so a test can state a filesystem as tar entries.
func buildLayerArtifact(t *testing.T, add func(tw *tar.Writer)) Artifact {
	return buildLayeredArtifact(t, add)
}

// buildLayeredArtifact is buildLayerArtifact over several layers, lowest
// first, so a test can state what a later layer does to an earlier one.
func buildLayeredArtifact(t *testing.T, layers ...func(tw *tar.Writer)) Artifact {
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()
	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(content),
			Size:      int64(len(content)),
		}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}
	var descs []ocispec.Descriptor
	for _, add := range layers {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		add(tw)
		require.NoError(t, tw.Close())
		descs = append(descs, push(ocispec.MediaTypeImageLayer, buf.Bytes()))
	}
	config, err := json.Marshal(ocispec.Image{})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    descs,
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	require.NoError(t, store.Tag(ctx, manifestDesc, "kit"))
	artifact, err := FromLayout(ctx, dir, "kit")
	require.NoError(t, err)
	return artifact
}

// A shell is usually a link to whatever implements it, so the metadata
// that decides whether it can be executed is the target's, not the
// link's.
func TestFileStatFollowsLinksToTheTarget(t *testing.T) {
	a := buildLayerArtifact(t, func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "bin/busybox", Typeflag: tar.TypeReg, Mode: 0o750, Uid: 7, Gid: 9, Size: 3,
		}))
		_, err := tw.Write([]byte("elf"))
		require.NoError(t, err)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "bin/sh", Typeflag: tar.TypeSymlink, Linkname: "busybox", Mode: 0o777,
		}))
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "bin/placeholder", Typeflag: tar.TypeReg, Mode: 0o644, Size: 0,
		}))
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "bin/node", Typeflag: tar.TypeChar, Mode: 0o777,
		}))
	})
	c, ok := a.(statChecker)
	require.True(t, ok)

	st, present, err := c.FileStat(context.Background(), "/bin/sh")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st, "the link's target answers")

	st, present, err = c.FileStat(context.Background(), "/bin/placeholder")
	require.NoError(t, err)
	require.True(t, present)
	require.Zero(t, st.Mode&0o111)

	// execve runs ordinary files, and a device node is not one however
	// its bits read.
	st, present, err = c.FileStat(context.Background(), "/bin/node")
	require.NoError(t, err)
	require.True(t, present)
	require.False(t, st.Regular)

	_, present, err = c.FileStat(context.Background(), "/bin/absent")
	require.NoError(t, err)
	require.False(t, present)
}

// A relative symlink resolves against the directory the lookup actually
// reached, which a symlinked ancestor can move.
func TestALinkResolvesAgainstWhereItsAncestorsLed(t *testing.T) {
	a := buildLayerArtifact(t, func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "long/a", Typeflag: tar.TypeSymlink, Linkname: "../x",
		}))
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "x/file", Typeflag: tar.TypeSymlink, Linkname: "../target",
		}))
		writeFile(t, tw, "target", 0o750, 7, 9)
		// What the lexical parent would have reached instead.
		writeFile(t, tw, "long/target", 0o644, 0, 0)
	})
	c := a.(statChecker)

	st, present, err := c.FileStat(context.Background(), "/long/a/file")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st)
}

// A hard link captures its target's inode at the moment the link applies,
// so the bits a later layer gives the target's path are another file's.
func TestFileStatOfAHardLinkIsTheInodeItCaptured(t *testing.T) {
	ctx := context.Background()

	t.Run("a later layer rewriting the target path", func(t *testing.T) {
		a := buildLayeredArtifact(t,
			func(tw *tar.Writer) {
				writeFile(t, tw, "bin/real", 0o750, 7, 9)
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name: "bin/sh", Typeflag: tar.TypeLink, Linkname: "bin/real",
				}))
			},
			func(tw *tar.Writer) {
				writeFile(t, tw, "bin/real", 0o600, 0, 0)
			})
		c := a.(statChecker)

		st, present, err := c.FileStat(ctx, "/bin/sh")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st)

		st, present, err = c.FileStat(ctx, "/bin/real")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o600, Regular: true}, st, "the path itself is the later file")
	})

	// A hard link is another name for the inode, so a relative symlink
	// target resolves against the directory of the name used to reach
	// it, not the directory the inode was first written in.
	t.Run("a link to a symlink resolves against the alias", func(t *testing.T) {
		a := buildLayerArtifact(t, func(tw *tar.Writer) {
			// Two files named real, in the two directories the same
			// inode can be reached through.
			writeFile(t, tw, "bin/real", 0o644, 0, 0)
			writeFile(t, tw, "usr/bin/real", 0o750, 7, 9)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "usr/bin/link", Typeflag: tar.TypeSymlink, Linkname: "real",
			}))
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "bin/sh", Typeflag: tar.TypeLink, Linkname: "usr/bin/link",
			}))
		})
		c := a.(statChecker)

		// /bin/sh names the symlink inode, and its relative "real"
		// resolves in /bin.
		st, present, err := c.FileStat(ctx, "/bin/sh")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o644, Regular: true}, st)

		// The same inode reached by its own name resolves in /usr/bin.
		st, present, err = c.FileStat(ctx, "/usr/bin/link")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st)
	})

	// The captured inode may come from a lower layer, which bounds where
	// it is found but not where following it may lead: a symlink still
	// resolves against the alias and the final state.
	t.Run("a link whose target is a symlink in a lower layer", func(t *testing.T) {
		a := buildLayeredArtifact(t,
			func(tw *tar.Writer) {
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name: "usr/bin/link", Typeflag: tar.TypeSymlink, Linkname: "real",
				}))
				writeFile(t, tw, "usr/bin/real", 0o750, 7, 9)
			},
			func(tw *tar.Writer) {
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name: "bin/sh", Typeflag: tar.TypeLink, Linkname: "usr/bin/link",
				}))
			},
			// Higher than the link: reachable when resolution continues
			// against the final state, invisible if it stops below.
			func(tw *tar.Writer) {
				writeFile(t, tw, "bin/real", 0o644, 0, 0)
			})
		c := a.(statChecker)

		st, present, err := c.FileStat(ctx, "/bin/sh")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o644, Regular: true}, st)
	})

	// Nothing a later layer does to the pathname the inode was captured
	// through un-creates the alias: the link is a name for the inode,
	// not for the path.
	t.Run("a later layer replacing an ancestor of the target", func(t *testing.T) {
		a := buildLayeredArtifact(t,
			func(tw *tar.Writer) {
				writeFile(t, tw, "usr/bin/real", 0o750, 7, 9)
			},
			func(tw *tar.Writer) {
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name: "bin/sh", Typeflag: tar.TypeLink, Linkname: "usr/bin/real",
				}))
			},
			func(tw *tar.Writer) {
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name: "usr/.wh..wh..opq", Typeflag: tar.TypeReg,
				}))
			})
		c := a.(statChecker)

		st, present, err := c.FileStat(ctx, "/bin/sh")
		require.NoError(t, err)
		require.True(t, present, "the alias outlives what happens to /usr")
		require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st)

		_, present, err = c.FileStat(ctx, "/usr/bin/real")
		require.NoError(t, err)
		require.False(t, present, "the pathname itself is gone")
	})

	t.Run("a same-layer rewrite after the link", func(t *testing.T) {
		a := buildLayerArtifact(t, func(tw *tar.Writer) {
			writeFile(t, tw, "bin/real", 0o750, 7, 9)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "bin/sh", Typeflag: tar.TypeLink, Linkname: "bin/real",
			}))
			writeFile(t, tw, "bin/real", 0o600, 0, 0)
		})
		c := a.(statChecker)

		st, present, err := c.FileStat(ctx, "/bin/sh")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, FileStat{Mode: 0o750, Uid: 7, Gid: 9, Regular: true}, st)
	})
}

// writeFile writes one ordinary file with the metadata a permission
// judgment reads.
func writeFile(t *testing.T, tw *tar.Writer, name string, mode int64, uid, gid int) {
	t.Helper()
	const body = "elf"
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeReg, Mode: mode, Uid: uid, Gid: gid, Size: int64(len(body)),
	}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
}

func writeDir(t *testing.T, tw *tar.Writer, name string, uid, gid int) {
	t.Helper()
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeDir, Mode: 0o755, Uid: uid, Gid: gid,
	}))
}

func writeLink(t *testing.T, tw *tar.Writer, name, target string) {
	t.Helper()
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777,
	}))
}

// The owner that counts is the directory entry's own, from the last layer
// that carries it; a file at the path is not a directory entry at all.
func TestDirStatReadsTheLastDirectoryEntry(t *testing.T) {
	a := buildLayeredArtifact(t,
		func(tw *tar.Writer) {
			writeDir(t, tw, "home/", 1000, 1000)
			writeDir(t, tw, "home/agent/", 0, 0)
			writeFile(t, tw, "etc", 0o644, 0, 0)
		},
		func(tw *tar.Writer) {
			writeDir(t, tw, "home/", 0, 0)
		},
	)
	w, ok := a.(overlayWalker)
	require.True(t, ok)
	ctx := context.Background()

	st, present, err := w.DirStat(ctx, "/home")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 0, st.Uid, "the later layer's entry replaces the earlier one")

	st, present, err = w.DirStat(ctx, "/home/agent")
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 0, st.Uid)

	_, present, err = w.DirStat(ctx, "/etc")
	require.NoError(t, err)
	require.False(t, present)

	_, present, err = w.DirStat(ctx, "/opt")
	require.NoError(t, err)
	require.False(t, present)
}

// A link resolves if the overlay carries what it points at — a file, a
// directory with or without an entry of its own, or another link that
// does — and dangles if the target is absent, deleted by a later layer,
// or a cycle.
func TestDanglingSymlinksAreTheOnesTheOverlayCannotResolve(t *testing.T) {
	a := buildLayeredArtifact(t,
		func(tw *tar.Writer) {
			writeDir(t, tw, "opt/", 0, 0)
			writeDir(t, tw, "opt/tool/", 0, 0)
			writeFile(t, tw, "opt/tool/tool", 0o755, 0, 0)
			writeFile(t, tw, "srv/implied/tool", 0o755, 0, 0)
			writeFile(t, tw, "opt/removed/tool", 0o755, 0, 0)
			writeLink(t, tw, "usr/local/bin/file", "../../../opt/tool/tool")
			writeLink(t, tw, "usr/local/bin/dir", "/opt/tool")
			writeLink(t, tw, "usr/local/bin/chain", "file")
			writeLink(t, tw, "usr/local/bin/implied", "/srv/implied")
			writeLink(t, tw, "usr/local/bin/root", "/")
			writeLink(t, tw, "usr/local/bin/stage", "/root/.local/share/tool/bin/tool")
			writeLink(t, tw, "usr/local/bin/removed", "/opt/removed/tool")
			writeLink(t, tw, "usr/local/bin/loop-a", "loop-b")
			writeLink(t, tw, "usr/local/bin/loop-b", "loop-a")
		},
		func(tw *tar.Writer) {
			writeFile(t, tw, "opt/.wh.removed", 0o644, 0, 0)
		},
	)
	w := a.(overlayWalker)

	dangling, err := w.DanglingSymlinks(context.Background())
	require.NoError(t, err)
	require.Equal(t, []Symlink{
		{Path: "/usr/local/bin/loop-a", Target: "loop-b"},
		{Path: "/usr/local/bin/loop-b", Target: "loop-a"},
		{Path: "/usr/local/bin/removed", Target: "/opt/removed/tool"},
		{Path: "/usr/local/bin/stage", Target: "/root/.local/share/tool/bin/tool"},
	}, dangling)
}

// An ancestor symlink with an empty target is dangling, and one targeting
// the root must join canonically instead of producing a double slash no
// archive path matches.
func TestAncestorLinkEdgeCases(t *testing.T) {
	build := buildLayerArtifact

	body := []byte(fixtureDescriptor)
	staged := "usr/share/sandbox/kit/demo/" + stagedDescriptorName

	t.Run("an empty-target ancestor is dangling", func(t *testing.T) {
		a := build(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: staged, Mode: 0o644, Size: int64(len(body)),
			}))
			_, err := tw.Write(body)
			require.NoError(t, err)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "usr/share/sandbox/kit/demo", Typeflag: tar.TypeSymlink, Linkname: "",
			}))
		})
		_, present, err := a.ReadFile(context.Background(), "/"+staged)
		require.NoError(t, err)
		require.False(t, present, "nothing beneath a dangling link is reachable")
	})

	t.Run("a dotted absolute target is cleaned", func(t *testing.T) {
		a := build(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "staged/" + stagedDescriptorName, Mode: 0o644, Size: int64(len(body)),
			}))
			_, err := tw.Write(body)
			require.NoError(t, err)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: staged, Typeflag: tar.TypeSymlink,
				Linkname: "/opt/../staged/" + stagedDescriptorName,
			}))
		})
		got, present, err := a.ReadFile(context.Background(), "/"+staged)
		require.NoError(t, err)
		require.True(t, present, "the dotted target names /staged in the image")
		require.Equal(t, body, got)
	})

	t.Run("a root-target ancestor joins canonically", func(t *testing.T) {
		a := build(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: stagedDescriptorName, Mode: 0o644, Size: int64(len(body)),
			}))
			_, err := tw.Write(body)
			require.NoError(t, err)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name: "usr/share/sandbox/kit/demo", Typeflag: tar.TypeSymlink, Linkname: "/",
			}))
		})
		got, present, err := a.ReadFile(context.Background(), "/"+staged)
		require.NoError(t, err)
		require.True(t, present, "the link targets the archive root")
		require.Equal(t, body, got)
	})
}

// An outer index with no annotations is an authoritative absence: the
// warning about it must not be suppressed by adopting a nested index's
// annotations during descent.
func TestABareOuterIndexIsNotRescuedByANestedOne(t *testing.T) {
	dir, tag := nestedLayoutFixture(t, nil)

	artifacts, err := FromLayoutAll(context.Background(), dir, tag)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	ann, hasIndex := artifacts[0].IndexAnnotations()
	require.True(t, hasIndex)
	require.Empty(t, ann, "the tagged index has no annotations, and that is the finding")
}

// A root-level opaque marker hides everything from lower layers, not just
// entries under some directory.
func TestARootOpaqueMarkerHidesEverything(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), ".wh..wh..opq")

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.False(t, present, "a root opaque marker hides the staged descriptor")

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems)
}

// The layout is <root>/<stem>/kit.yaml with exactly one stem segment: a
// descriptor buried deeper is an unrelated file, and treating it as a
// staged kit would let an artifact with nothing at the specified shape
// pass staged-source validation.
func TestADeeperDescriptorIsNotAStagedStem(t *testing.T) {
	dir, tag := layoutFixture(t,
		path.Join("usr/share/sandbox/kit/foo/bar", stagedDescriptorName),
		[]byte(fixtureDescriptor))

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems, "foo/bar is two segments, not a stem")
}

func TestFromLayoutReadsAConformingKit(t *testing.T) {
	dir, tag := layoutFixture(t,
		path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName),
		[]byte(fixtureDescriptor))

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	rep, err := Run(context.Background(), artifact)
	require.NoError(t, err)
	require.Empty(t, rep.Findings, "fixture must conform: %s", rep)
}

// Stem discovery walks the layers, because a descriptor carries no name —
// identity is the reference a kit is consumed by.
func TestFromLayoutFindsTheStagedStem(t *testing.T) {
	dir, tag := layoutFixture(t,
		path.Join("usr/share/sandbox/kit/some-other-stem", stagedDescriptorName),
		[]byte(fixtureDescriptor))

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"some-other-stem"}, stems)
}

// An image whose layers stage nothing is not self-describing, which the
// staged-sources check exists to catch on a real artifact.
func TestFromLayoutReportsAKitThatStagedNothing(t *testing.T) {
	dir, tag := layoutFixture(t, "etc/motd", []byte(fixtureDescriptor))

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	rep, err := Run(context.Background(), artifact)
	require.NoError(t, err)
	require.True(t, rep.Failed())
	require.ErrorContains(t, rep.Err(), "self-describing")
}

// A reference is typed the way it is typed everywhere else, so the
// shorthand forms have to reach the registry they name rather than making
// the first path segment a host.
func TestSplitRef(t *testing.T) {
	const dgst = "sha256:0ba2d5c53a3f8e2f3b6a1a2b8cd0e4f5061728394a5b6c7d8e9f0a1b2c3d4e5f"
	for ref, want := range map[string][2]string{
		"docker.io/me/kit:1.0.0":     {"docker.io/me/kit", "1.0.0"},
		"reg.example.com:5000/x:tag": {"reg.example.com:5000/x", "tag"},
		"docker.io/me/kit@" + dgst:   {"docker.io/me/kit", dgst},
		// Hub shorthand: a user repository, and an official image whose
		// repository is implicitly under library.
		"me/kit:1.0.0": {"docker.io/me/kit", "1.0.0"},
		"kit:1.0.0":    {"docker.io/library/kit", "1.0.0"},
		// A bare name is the tag every other tool reads it as.
		"me/kit": {"docker.io/me/kit", "latest"},
		// A host stays a host: normalization must not swallow the
		// registry a local build loop pushes to.
		"localhost:5000/me/kit:1.0.0": {"localhost:5000/me/kit", "1.0.0"},
		"127.0.0.1:5000/me/kit":       {"127.0.0.1:5000/me/kit", "latest"},
		// Pinned twice: the digest is what the reference named.
		"me/kit:1.0.0@" + dgst: {"docker.io/me/kit", dgst},
	} {
		repo, tag, err := splitRef(ref)
		require.NoError(t, err, ref)
		require.Equal(t, want[0], repo, ref)
		require.Equal(t, want[1], tag, ref)
	}

	// A reference no registry could serve is refused here, where the
	// error can name it, rather than as a request that goes nowhere.
	for _, ref := range []string{"", "Me/Kit:1.0.0", "me/kit:", "me/kit@sha256:ab"} {
		_, _, err := splitRef(ref)
		require.Error(t, err, ref)
	}
}

// A whiteout in a later layer deletes what an earlier one staged. Without
// honouring it, an artifact could pass the staged-source checks for a file
// that is absent from the filesystem a runtime would actually see.
func TestALaterLayerCanDeleteTheStagedDescriptor(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	whiteout := path.Join("usr/share/sandbox/kit/demo", ".wh."+stagedDescriptorName)
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), whiteout)

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems, "a deleted descriptor must not be discoverable")

	rep, err := Run(context.Background(), artifact)
	require.NoError(t, err)
	require.True(t, rep.Failed(), "a kit whose sources were deleted does not conform")
}

// An opaque directory marker hides everything the lower layers put there.
func TestAnOpaqueDirectoryHidesStagedSources(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	opaque := "usr/share/sandbox/kit/demo/.wh..wh..opq"
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), opaque)

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.False(t, present, "an opaque directory hides what lower layers staged")
}

// A whiteout is an entry whose basename starts with .wh., not any path
// containing that substring: a kit legitimately named foo.wh.bar must not
// have its sources vanish from the inventory.
func TestAStemContainingWhSubstringIsNotAWhiteout(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/foo.wh.bar", stagedDescriptorName)
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor))

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"foo.wh.bar"}, stems)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.True(t, present, "an ordinary name containing .wh. is not a deletion")
}

// Deleting a directory removes everything under it, so a whiteout on the
// staged kit root hides the descriptor as surely as one on the file.
func TestAnAncestorWhiteoutHidesStagedSources(t *testing.T) {
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	ancestor := "usr/share/sandbox/kit/.wh.demo"
	dir, tag := layoutFixture(t, staged, []byte(fixtureDescriptor), ancestor)

	artifact, err := FromLayout(context.Background(), dir, tag)
	require.NoError(t, err)

	_, present, err := artifact.ReadFile(context.Background(), "/"+staged)
	require.NoError(t, err)
	require.False(t, present, "deleting the directory deletes what is inside it")

	stems, err := artifact.StagedStems(context.Background())
	require.NoError(t, err)
	require.Empty(t, stems)
}

// nestedLayoutFixture wraps a kit in two indexes, the shape a registry
// serves when the tagged artifact fronts another index.
func nestedLayoutFixture(t *testing.T, outerAnnotations map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := oci.New(dir)
	require.NoError(t, err)
	ctx := context.Background()

	push := func(mediaType string, content []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(content), Size: int64(len(content))}
		require.NoError(t, store.Push(ctx, d, bytes.NewReader(content)))
		return d
	}

	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	staged := path.Join("usr/share/sandbox/kit/demo", stagedDescriptorName)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: staged, Mode: 0o644, Size: int64(len(fixtureDescriptor))}))
	_, err = tw.Write([]byte(fixtureDescriptor))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	layerDesc := push(ocispec.MediaTypeImageLayer, layer.Bytes())

	config, err := json.Marshal(ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"},
		Config:   ocispec.ImageConfig{Entrypoint: []string{"bash"}},
	})
	require.NoError(t, err)
	configDesc := push(ocispec.MediaTypeImageConfig, config)

	d, err := spec.Decode([]byte(fixtureDescriptor))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: d.SchemaVersion,
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specsgo.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		Config: configDesc, Layers: []ocispec.Descriptor{layerDesc}, Annotations: ann,
	})
	require.NoError(t, err)
	manifestDesc := push(ocispec.MediaTypeImageManifest, manifest)
	manifestDesc.Platform = &ocispec.Platform{OS: "linux", Architecture: "arm64"}

	// The inner index repeats the manifest's annotations; only the outer
	// one is wrong, and only the outer one a registry serves for the tag.
	inner, err := json.Marshal(ocispec.Index{
		Versioned: specsgo.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{manifestDesc}, Annotations: ann,
	})
	require.NoError(t, err)
	innerDesc := push(ocispec.MediaTypeImageIndex, inner)

	outer, err := json.Marshal(ocispec.Index{
		Versioned: specsgo.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{innerDesc}, Annotations: outerAnnotations,
	})
	require.NoError(t, err)
	outerDesc := push(ocispec.MediaTypeImageIndex, outer)
	require.NoError(t, store.Tag(ctx, outerDesc, "kit"))
	return dir, "kit"
}

// The tagged index is the one a registry serves and a consumer reads
// first, so descending into a nested index must not swap it for the inner
// one — which would leave the visible annotations unchecked.
func TestNestedIndexKeepsTheTaggedIndexAnnotations(t *testing.T) {
	dir, tag := nestedLayoutFixture(t, map[string]string{
		spec.AnnotationDescriptor:    `{"schemaVersion":"3","kind":"mixin"}`,
		spec.AnnotationSchemaVersion: "3",
	})

	artifacts, err := FromLayoutAll(context.Background(), dir, tag)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	rep, err := Run(context.Background(), artifacts[0])
	require.NoError(t, err)
	require.True(t, rep.Failed(), "the outer index disagrees with the manifest and must be caught:\n%s", rep)
	require.ErrorContains(t, rep.Err(), "disagrees")
}

// A loopback registry serves plain HTTP without being asked: the request
// never leaves the host. Everything else has to opt in, because there the
// absence of TLS is a property of a network someone else can be on.
func TestPlainHTTPIsImpliedForLoopbackOnly(t *testing.T) {
	for _, host := range []string{
		"localhost", "localhost:5000",
		"127.0.0.1", "127.0.0.1:5000", "127.9.9.9:5000",
		"[::1]:5000", "::1",
	} {
		require.True(t, isLoopbackRegistry(host), "%s is this machine", host)
	}
	for _, host := range []string{
		"docker.io", "registry.example.com:5000",
		"10.0.0.1:5000", "192.168.1.10", "[2001:db8::1]:5000",
		"localhost.example.com", "notlocalhost",
	} {
		require.False(t, isLoopbackRegistry(host), "%s is somewhere else", host)
	}
}

// The option carries a caller's decision to a repository that would
// otherwise have been held to HTTPS.
func TestWithPlainHTTPReachesANonLoopbackRegistry(t *testing.T) {
	r, err := remoteRepository("registry.example.com/kit", registryOptions{})
	require.NoError(t, err)
	require.False(t, r.PlainHTTP, "a remote registry is HTTPS unless the caller says otherwise")

	r, err = remoteRepository("registry.example.com/kit", resolveOptions([]RegistryOption{WithPlainHTTP()}))
	require.NoError(t, err)
	require.True(t, r.PlainHTTP)

	r, err = remoteRepository("localhost:5000/kit", registryOptions{})
	require.NoError(t, err)
	require.True(t, r.PlainHTTP, "a loopback registry needs no flag")
}

// An index nested inside an index states no platform of its own, so
// the search descends — and backtracks out of a branch that does not
// hold the platform rather than committing to the first one it sees.
func TestFindPlatformManifestSearchesBranches(t *testing.T) {
	want := &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	amd := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: "sha256:amd",
		Platform: &ocispec.Platform{OS: "linux", Architecture: "amd64"}}
	arm := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: "sha256:arm", Platform: want}

	// Stated at this level.
	flat := &ocispec.Index{Manifests: []ocispec.Descriptor{amd, arm}}
	got, err := findPlatformManifest(context.Background(), &indexFetcher{}, flat, want, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "sha256:arm", string(got.Digest))

	// Absent is absence, not another platform.
	got, err = findPlatformManifest(context.Background(), &indexFetcher{},
		&ocispec.Index{Manifests: []ocispec.Descriptor{amd}}, want, 0)
	require.NoError(t, err)
	require.Nil(t, got)

	// In the second of two platform-less branches: the first has to be
	// searched and abandoned.
	f := &indexFetcher{indexes: map[string]ocispec.Index{
		"sha256:first":  {Manifests: []ocispec.Descriptor{amd}},
		"sha256:second": {Manifests: []ocispec.Descriptor{arm}},
	}}
	branched := &ocispec.Index{Manifests: []ocispec.Descriptor{
		{MediaType: ocispec.MediaTypeImageIndex, Digest: "sha256:first"},
		{MediaType: ocispec.MediaTypeImageIndex, Digest: "sha256:second"},
	}}
	got, err = findPlatformManifest(context.Background(), f, branched, want, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "sha256:second", string(got.Digest))
}

// indexFetcher serves nested indexes by digest.
type indexFetcher struct {
	indexes map[string]ocispec.Index
}

func (f *indexFetcher) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{}, errors.New("not used")
}

func (f *indexFetcher) Fetch(_ context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	index, ok := f.indexes[string(desc.Digest)]
	if !ok {
		return nil, fmt.Errorf("no such index %s", desc.Digest)
	}
	body, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// BuildKit records a single-platform arm64 image with an empty variant
// while an index commonly spells the same platform arm64/v8. Reading
// those as different would leave the declaration check skipping a set
// whose inputs are right there.
func TestPlatformMatchingNormalizesDefaultVariants(t *testing.T) {
	bare := &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	v8 := &ocispec.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	require.True(t, samePlatform(*bare, *v8))
	require.True(t, samePlatform(
		ocispec.Platform{OS: "linux", Architecture: "amd64"},
		ocispec.Platform{OS: "linux", Architecture: "amd64", Variant: "v1"}))

	// A genuinely different variant is still different.
	require.False(t, samePlatform(
		ocispec.Platform{OS: "linux", Architecture: "arm", Variant: "v7"},
		ocispec.Platform{OS: "linux", Architecture: "arm", Variant: "v8"}))
	require.False(t, samePlatform(*bare, ocispec.Platform{OS: "linux", Architecture: "amd64"}))
	require.False(t, samePlatform(*bare, ocispec.Platform{OS: "windows", Architecture: "arm64"}))

	// And the search finds the manifest either way round.
	index := &ocispec.Index{Manifests: []ocispec.Descriptor{
		{MediaType: ocispec.MediaTypeImageManifest, Digest: "sha256:arm", Platform: v8},
	}}
	got, err := findPlatformManifest(context.Background(), &indexFetcher{}, index, bare, 0)
	require.NoError(t, err)
	require.NotNil(t, got, "an index spelling it v8 answers for a bare arm64")
	require.Equal(t, "sha256:arm", string(got.Digest))
}
