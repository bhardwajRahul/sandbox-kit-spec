package kit

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// fake is an artifact assembled in memory, so each check can be shown to
// catch the thing it exists for.
type fake struct {
	annotations map[string]string
	config      ocispec.Image
	layers      []ocispec.Descriptor
	layersKnown bool
	files       map[string][]byte
	indexAnn    map[string]string
	hasIndex    bool
	// kits are the artifacts a merged set lists, keyed by reference,
	// so the declaration check has something to compare against.
	kits map[string]Artifact
}

func (f *fake) ResolveKit(_ context.Context, ref, _ string) (Artifact, error) {
	if a, ok := f.kits[ref]; ok {
		return a, nil
	}
	return nil, errNoRegistry
}

func (f *fake) Annotations() map[string]string { return f.annotations }

func (f *fake) Config(context.Context) (*ocispec.Image, error) { return &f.config, nil }

func (f *fake) Layers(context.Context) ([]ocispec.Descriptor, bool, error) {
	return f.layers, f.layersKnown, nil
}

func (f *fake) ReadFile(_ context.Context, name string) ([]byte, bool, error) {
	body, ok := f.files[name]
	return body, ok, nil
}

func (f *fake) StagedStems(context.Context) ([]string, error) {
	var names []string
	for p := range f.files {
		if stems := stemsFromPaths([]string{p[1:]}); len(stems) == 1 {
			names = append(names, stems[0])
		}
	}
	return names, nil
}

func (f *fake) IndexAnnotations() (map[string]string, bool) { return f.indexAnn, f.hasIndex }

const stem = "demo"

// conforming builds a mixin that passes every check, for tests to break
// one thing at a time.
func conforming(t *testing.T) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	return &fake{
		annotations: ann,
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
		},
	}
}

func findings(t *testing.T, a Artifact) map[string]report.Finding {
	t.Helper()
	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	out := map[string]report.Finding{}
	for _, f := range rep.Findings {
		out[f.Check] = f
	}
	return out
}

func TestConformingKitHasNoFindings(t *testing.T) {
	rep, err := Run(context.Background(), conforming(t))
	require.NoError(t, err)
	require.Empty(t, rep.Findings, "a conforming kit must produce nothing to report")
	require.False(t, rep.Failed())
	require.NoError(t, rep.Err())
}

func TestAnArtifactWithoutTheDescriptorAnnotationIsNotAKit(t *testing.T) {
	a := conforming(t)
	delete(a.annotations, spec.AnnotationDescriptor)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-annotation"].Severity)
	require.Contains(t, got["descriptor-annotation"].Detail, "not a kit")
}

func TestSchemaVersionAnnotationMustMatchTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationSchemaVersion] = "2"

	require.Equal(t, report.Fail, findings(t, a)["schema-version-annotation"].Severity)
}

// The capabilities annotation is an index, never a second source, so it
// has to agree with the descriptor exactly — including being absent when
// the kit asks for nothing.
func TestCapabilitiesAnnotationMustMatchTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationCapabilities] = "com.docker.sandbox/port@1"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["capabilities-annotation"].Severity)
	require.Contains(t, got["capabilities-annotation"].Detail, "requests no capabilities")
}

func TestOCIAnnotationsMustBeDerivedFromTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.OCIAnnotationTitle] = "Something Else"

	require.Equal(t, report.Fail, findings(t, a)["oci-annotations"].Severity)
}

// A wall-clock stamp would break build reproducibility, so publishing
// deliberately omits it.
func TestCreatedAnnotationMustNotBeEmitted(t *testing.T) {
	a := conforming(t)
	a.annotations["org.opencontainers.image.created"] = "2026-01-01T00:00:00Z"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["oci-annotations"].Severity)
	require.Contains(t, got["oci-annotations"].Detail, "must not be emitted")
}

// base.* sits beside created and revision in §9.3's deliberately-not-
// emitted list: base-image state is the builder's knowledge, recorded in
// provenance, never the descriptor's.
func TestBaseImageAnnotationsMustNotBeEmitted(t *testing.T) {
	for _, key := range []string{
		"org.opencontainers.image.base.name",
		"org.opencontainers.image.base.digest",
	} {
		a := conforming(t)
		a.annotations[key] = "docker.io/library/debian:trixie"

		got := findings(t, a)
		require.Equal(t, report.Fail, got["oci-annotations"].Severity, key)
		require.Contains(t, got["oci-annotations"].Detail, "must not be emitted", key)
	}
}

// The OCI image-manifest schema requires a layer, and staging the kit's
// own sources is what guarantees one.
func TestAZeroLayerManifestFails(t *testing.T) {
	a := conforming(t)
	a.layers = nil

	require.Equal(t, report.Fail, findings(t, a)["at-least-one-layer"].Severity)
}

// A build has not assembled layers yet; the check is skipped there rather
// than reported as an empty manifest.
func TestLayerCountIsSkippedWhenTheSourceCannotSeeLayers(t *testing.T) {
	a := conforming(t)
	a.layers, a.layersKnown = nil, false

	require.Equal(t, report.Skip, findings(t, a)["at-least-one-layer"].Severity)
}

// The regression this suite exists for: staging silently not running.
func TestMissingStagedSourcesFail(t *testing.T) {
	a := conforming(t)
	a.files = map[string][]byte{}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "self-describing")
}

// The annotation carries compact JSON and the staged file the expanded
// YAML, so agreement is about the declarations rather than the bytes.
func TestStagedDescriptorMustDescribeTheSameKit(t *testing.T) {
	a := conforming(t)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: workload\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "different kits")
}

func TestADeclaredRecipeMustBeStaged(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\nbuild: |\n  FROM scratch\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-recipe"].Severity)
	require.Contains(t, got["staged-recipe"].Detail, stagedRecipeName)
}

// Publishing rewrites contentFile to the staged path; an authored path
// surviving into the published descriptor means the body never shipped.
func TestAgentContextMustPointAtAStagedFile(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" +
		"  - type: com.docker.sandbox/agent-context@1\n    config:\n      contentFile: ./context.md\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.annotations[spec.AnnotationCapabilities] = spec.CapabilityTypes(d.Capabilities)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["agent-context-staged"].Severity)
	require.Contains(t, got["agent-context-staged"].Detail, "rewrites it")
}

// A workload runs under a bare docker run, minus what the descriptor
// declares; with no entrypoint or cmd there is nothing to degrade to.
func TestAWorkloadNeedsALaunchConfig(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: workload\nprovides: [\"demo@1.0.0\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["workload-launch-config"].Severity)

	a.config.Config.Entrypoint = []string{"bash"}
	require.NotContains(t, findings(t, a), "workload-launch-config")
}

// Index annotations are an optimization a multi-node builder legitimately
// dissolves, and consumers must fall back to the platform manifest — so
// their absence is worth saying, not worth failing.
func TestMissingIndexAnnotationsWarnRatherThanFail(t *testing.T) {
	a := conforming(t)
	a.hasIndex, a.indexAnn = true, map[string]string{}

	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	require.False(t, rep.Failed())
	require.Equal(t, report.Warn, findings(t, a)["index-annotations"].Severity)
}

// Pointing at another kit's staged directory would make the body
// something this artifact does not carry, so being under the shared root
// is not enough.
func TestAgentContextMustSitBesideThisKitsSources(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" +
		"  - type: com.docker.sandbox/agent-context@1\n    config:\n" +
		"      contentFile: /usr/share/sandbox/kit/other/context.md\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.annotations[spec.AnnotationCapabilities] = spec.CapabilityTypes(d.Capabilities)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)
	a.files["/usr/share/sandbox/kit/other/context.md"] = []byte("someone else's body")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["agent-context-staged"].Severity)
}

// An index that carries annotations must carry the same ones: a consumer
// reading it would otherwise judge a different kit than it runs.
func TestIndexAnnotationsMustAgreeWithTheManifest(t *testing.T) {
	a := conforming(t)
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    `{"schemaVersion":"3","kind":"workload"}`,
		spec.AnnotationSchemaVersion: "3",
	}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["index-annotations"].Severity)
	require.Contains(t, got["index-annotations"].Detail, "disagrees")
}

// Presence is meaning for promoted annotations: an index saying
// capabilities="" claims "none requested", which it must not invent when
// the manifest carries no such key at all.
func TestAnIndexCannotInventAPromotedAnnotation(t *testing.T) {
	a := conforming(t)
	require.NotContains(t, a.annotations, spec.AnnotationCapabilities,
		"the fixture must not request capabilities for this test to mean anything")
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    a.annotations[spec.AnnotationDescriptor],
		spec.AnnotationSchemaVersion: a.annotations[spec.AnnotationSchemaVersion],
		spec.AnnotationCapabilities:  "",
	}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["index-annotations"].Severity)
	require.Contains(t, got["index-annotations"].Detail, "manifest does not")
}

// An index repeating the manifest's annotations is the normal case.
func TestAgreeingIndexAnnotationsPass(t *testing.T) {
	a := conforming(t)
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    a.annotations[spec.AnnotationDescriptor],
		spec.AnnotationSchemaVersion: a.annotations[spec.AnnotationSchemaVersion],
	}

	require.NotContains(t, findings(t, a), "index-annotations")
}

// Publishing emits no key for an empty field, so an annotation the
// descriptor cannot account for is as wrong as a mismatched one.
func TestAnUnaccountedOCIAnnotationFails(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.OCIAnnotationDescription] = "invented out of nowhere"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["oci-annotations"].Severity)
	require.Contains(t, got["oci-annotations"].Detail, "is empty")
}

// Comparing a hand-picked subset would let the staged descriptor differ in
// everything unlisted, so agreement is over the whole document.
func TestStagedDescriptorMustAgreeInEveryField(t *testing.T) {
	a := conforming(t)
	// Same kind, provides, and capabilities; different display metadata.
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: mixin\ndisplayName: Something Else\nprovides: [\"demo@1.0.0\"]\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "different kits")
}

// mergedSet builds a conforming merged set: a kit whose descriptor
// records the kits it was merged from, carrying their staged sources
// beside its own.
func mergedSet(t *testing.T, kits ...string) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo Set\nversion: \"1.0.0\"\nprovides: [\"demo@1.0.0\""
	for _, name := range kits {
		authored += ", \"" + name + "@1.0.0\""
	}
	authored += "]\nkits:\n"
	for _, name := range kits {
		authored += "  - ref: reg.example.com/sbx-kit-" + name + ":1.0.0\n    digest: sha256:" +
			"1111111111111111111111111111111111111111111111111111111111111111\n"
	}
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	f := &fake{
		annotations: ann,
		config:      ocispec.Image{Config: ocispec.ImageConfig{Entrypoint: []string{"/bin/sh"}}},
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
		},
	}
	// Each merged kit's own sources ride along, which is what the
	// staged-roots check counts, and each is reachable as an artifact
	// of its own, which is what the declaration check compares against.
	f.kits = map[string]Artifact{}
	for _, name := range kits {
		listed := "schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [\"" + name + "@1.0.0\"]\n"
		f.files[path.Join(StagedKitRoot, name, stagedDescriptorName)] = []byte(listed)
		ld, err := spec.Decode([]byte(listed))
		require.NoError(t, err)
		lp, err := json.Marshal(ld)
		require.NoError(t, err)
		f.kits["reg.example.com/sbx-kit-"+name+":1.0.0"] = &fake{
			annotations: map[string]string{spec.AnnotationDescriptor: string(lp)},
		}
	}
	return f
}

// restate rewrites one listed kit's published descriptor in both
// places a merged set carries it: the artifact the set pins by digest,
// and the sources staged into the merged filesystem. They are one
// document in reality, so a fixture that moved only one would be
// testing a kit that cannot exist.
func restate(t *testing.T, a *fake, name string, mutate func(*spec.Descriptor)) {
	t.Helper()
	listed := a.kits["reg.example.com/sbx-kit-"+name+":1.0.0"].(*fake)
	d, err := spec.Decode([]byte(listed.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	mutate(d)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	listed.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, name, stagedDescriptorName)] = published
}

func TestAMergedSetConforms(t *testing.T) {
	rep, err := Run(context.Background(), mergedSet(t, "shell", "gh"))
	require.NoError(t, err)
	require.Empty(t, rep.Findings, "a conforming merged set must produce nothing to report")
}

// The staged sources of the kits a set lists are the only place a
// consumer can read their declarations without fetching them, so a
// merged kit that dropped them describes content nobody can inspect.
func TestAMergedSetMustCarryItsKitsStagedSources(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	delete(a.files, path.Join(StagedKitRoot, "gh", stagedDescriptorName))

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set"].Severity,
		"a warning would let the artifact pass: Report.Err counts only failures")
	require.Contains(t, got["merged-set"].Detail, "at least 3 staged roots are expected")
}

// A set's content is the kits it lists, so a recipe beside them
// describes content the artifact does not carry.
func TestAMergedSetMustNotAlsoDeclareARecipe(t *testing.T) {
	a := mergedSet(t, "shell")
	d, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	d.Build = "FROM scratch\n"
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set"].Severity)
	require.Contains(t, got["merged-set"].Detail, "content comes from exactly one of them")
}

// kind: set is an authoring kind the frontend resolves away; reaching a
// consumer means the merge never ran, so the layers are not what the
// descriptor describes.
func TestAPublishedSetKindIsRefused(t *testing.T) {
	a := mergedSet(t, "shell")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `"kind":"workload"`, `"kind":"set"`, 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-valid"].Severity)
	require.Contains(t, got["descriptor-valid"].Detail, "kind: set")
}

// Every listed kit is pinned: an authored entry names a version by tag,
// a published one records the manifest the build resolved.
func TestAPublishedSetPinsEveryKit(t *testing.T) {
	a := mergedSet(t, "shell")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `,"digest":"sha256:`+strings.Repeat("1", 64)+`"`, "", 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-valid"].Severity)
	require.Contains(t, got["descriptor-valid"].Detail, "with no digest")
}

// The merge is checkable against its inputs, not only against itself:
// each listed kit is fetched at the digest the set recorded, and what
// it offered has to appear in the merged descriptor.
func TestAMergedSetCarriesItsKitsProvides(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `,"gh@1.0.0"`, "", 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "provides gh@1.0.0")
}

// A merged kit ships its kits' content, so it reports their terms.
func TestAMergedSetCarriesItsKitsLicenses(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) { d.Licenses = []string{"MIT"} })

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "licensed MIT")
}

// A requirement the set answers itself is gone from the merged
// descriptor — keeping it would publish a kit that can never resolve,
// since a kit cannot satisfy its own requirement.
func TestAMergedSetDropsRequirementsItAnswers(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	restate(t, a, "gh", func(d *spec.Descriptor) { d.Requires = []string{"shell >= 1.0.0"} })

	// The set answers it, and the merged descriptor does not restate
	// it: nothing to report.
	require.NotContains(t, findings(t, a), "merged-set-declarations")

	// Restating it is the failure.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Requires = []string{"shell >= 1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "can never resolve")
}

// Whether a dropped requirement was answered is not decidable from
// outside: the merged descriptor does not distinguish the set's own
// provides from its kits', so an entry the listed kits do not answer
// may still have been answered by the set itself. The check reports
// only the direction that is decidable — an entry the kits answer and
// the merge kept, which nothing can ever satisfy.
func TestTheDeclarationCheckDoesNotJudgeDroppedRequirements(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) { d.Requires = []string{"team >= 1.0.0"} })

	// The merged descriptor drops it, as it would when the set's own
	// provides answered it. Nothing to report.
	require.NotContains(t, findings(t, a), "merged-set-declarations")

	// Retaining one the listed kits answer is the decidable failure.
	b := mergedSet(t, "shell", "gh")
	restate(t, b, "gh", func(d *spec.Descriptor) { d.Requires = []string{"shell >= 1.0.0"} })
	setD, err := spec.Decode([]byte(b.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Requires = []string{"shell >= 1.0.0"}
	published, err := json.Marshal(setD)
	require.NoError(t, err)
	b.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, b)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "can never resolve")
}

// A source with no registry behind it — an OCI layout holds one
// artifact — cannot reach the kits a set lists, and says so rather
// than reporting the merge unchecked as correct.
func TestTheDeclarationCheckSkipsWhenTheKitsAreUnreachable(t *testing.T) {
	a := mergedSet(t, "shell")
	a.kits = nil

	got := findings(t, a)
	require.Equal(t, report.Skip, got["merged-set-declarations"].Severity)
	require.NoError(t, report.Report{Findings: []report.Finding{got["merged-set-declarations"]}}.Err(),
		"a skip is not a failure")
}

// A bare provide takes the version its consumption reference carries
// before the descriptor's own, so a set listing base:2.0.0 whose
// descriptor still says 1.0.0 merged base@2.0.0. Reading the
// descriptor alone would report that conforming set as wrong.
func TestTheDeclarationCheckUsesTheEffectiveVersion(t *testing.T) {
	a := mergedSet(t, "shell")
	// Offers a bare name, and a version its reference has outgrown.
	restate(t, a, "shell", func(d *spec.Descriptor) {
		d.Provides = []string{"shell"}
		d.Version = "0.9.0"
	})

	// The merged descriptor carries what the reference names.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Provides = []string{"demo@1.0.0", "shell@1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	require.NotContains(t, findings(t, a), "merged-set-declarations",
		"shell@1.0.0 comes from the tag, not the stale descriptor")
}

// §9.5 applies the internal-drop rule to integrates as it does to
// requires, so a set retaining one its kits answer is as unresolvable.
func TestTheDeclarationCheckCoversIntegrates(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	restate(t, a, "gh", func(d *spec.Descriptor) { d.Integrates = []string{"shell >= 1.0.0"} })

	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Integrates = []string{"shell >= 1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "still integrates")
}

// Counting roots proves nothing on its own: an artifact can stage as
// many unrelated roots as it lists kits and carry none of their
// sources. Each listed kit has to be findable among them.
func TestAMergedSetBindsEachKitToAStagedRoot(t *testing.T) {
	a := mergedSet(t, "shell", "gh")

	// Replace one kit's staged sources with an unrelated kit's. The
	// count is unchanged, so only the binding can catch it.
	a.files[path.Join(StagedKitRoot, "gh", stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [\"decoy@1.0.0\"]\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "sbx-kit-gh:1.0.0 is listed but its staged sources are not present")

	stems, err := a.StagedStems(context.Background())
	require.NoError(t, err)
	require.Len(t, stems, 3, "and the count alone would still have passed")
}

// A re-export is checkable from the artifact alone — the merged
// descriptor keeps kits[].args and the set's own args — and what the
// rule protects is the installer, who sees only the set's declaration.
func TestTheDeclarationCheckJudgesReExports(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) {
		d.Args = map[string]spec.Arg{"mode": {Enum: []string{"fast", "slow"}, Required: true}}
	})

	// The set re-exports it through an arg that keeps the requirement
	// but drops the enum, so the enum is the only thing missing.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Args = map[string]spec.Arg{"mode": {Required: true}}
	setD.Kits[0].Args = map[string]string{"mode": "${{ kit.args.mode }}"}
	published, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "does not restate its enum")

	// Restating the contract is what makes it pass.
	setD.Args = map[string]spec.Arg{"mode": {Enum: []string{"fast", "slow"}, Required: true}}
	published, err = json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	require.NotContains(t, findings(t, a), "merged-set-declarations")
}
