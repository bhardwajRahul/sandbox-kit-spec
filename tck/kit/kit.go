// Package kit checks whether an artifact is a conforming kit.
//
// The checks run in two places against the same definitions. The BuildKit
// frontend runs the ones observable before export, so `docker buildx
// build` fails on a malformed kit instead of publishing one; `kit-tck kit
// <ref>` runs all of them against a published image, which is the only way
// to see what the exporter and the registry actually did — and the only
// way to judge an artifact this frontend did not build.
package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// StagedKitRoot is where every kit stages its own sources.
const StagedKitRoot = "/usr/share/sandbox/kit"

const (
	stagedDescriptorName = "kit.yaml"
	stagedRecipeName     = "kit.dockerfile"
)

// Artifact is the kit under test, however it is reached: a registry, an
// OCI layout, or the filesystem a build is about to export. Whatever a
// source cannot see it reports as unavailable rather than as absent, so a
// check is skipped where it cannot apply instead of failing there.
type Artifact interface {
	// Annotations are the image manifest's, or the ones a build is about
	// to set.
	Annotations() map[string]string

	// Config is the image config the artifact runs under.
	Config(context.Context) (*ocispec.Image, error)

	// Layers describes the manifest's layers. A build has not assembled
	// them yet and reports false.
	Layers(context.Context) ([]ocispec.Descriptor, bool, error)

	// ReadFile returns a path from the composed filesystem, reporting
	// false when nothing is there.
	ReadFile(context.Context, string) ([]byte, bool, error)

	// StagedStems lists the kit roots under StagedKitRoot.
	StagedStems(context.Context) ([]string, error)

	// IndexAnnotations are the annotations of the index the manifest hangs
	// under, when the artifact has one.
	IndexAnnotations() (map[string]string, bool)
}

// check is one conformance rule.
type check struct {
	name        string
	requirement string
	run         func(context.Context, *state) []report.Finding
}

// state is what the checks share: the artifact plus the descriptor every
// one of them starts from, decoded once.
type state struct {
	artifact   Artifact
	descriptor *spec.Descriptor
	stem       string
}

// Run judges an artifact against every check.
func Run(ctx context.Context, a Artifact) (report.Report, error) {
	raw := a.Annotations()[spec.AnnotationDescriptor]
	if raw == "" {
		return report.Report{Findings: []report.Finding{{
			Check:       "descriptor-annotation",
			Requirement: "SPEC-v3 §9.3",
			Severity:    report.Fail,
			Detail:      fmt.Sprintf("manifest carries no %s annotation; this is not a kit", spec.AnnotationDescriptor),
		}}}, nil
	}
	d, err := spec.Decode([]byte(raw))
	if err != nil {
		return report.Report{Findings: []report.Finding{{
			Check:       "descriptor-annotation",
			Requirement: "SPEC-v3 §9.3",
			Severity:    report.Fail,
			Detail:      fmt.Sprintf("descriptor annotation does not decode: %v", err),
		}}}, nil
	}

	s := &state{artifact: a, descriptor: d}
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return report.Report{}, fmt.Errorf("list staged kit roots: %w", err)
	}
	s.stem = ownStem(ctx, a, d, stems)

	var rep report.Report
	for _, c := range checks {
		rep.Add(c.name, c.requirement, c.run(ctx, s)...)
	}
	return rep, nil
}

// ownStem picks the staged root holding THIS kit's sources.
//
// One root is the ordinary case and needs no judgment. Several mean the
// artifact carries other kits' sources too — a kit built FROM a kit, or
// a merged set, whose layers include the staged sources of every kit it
// merged — and
// then the kit's own root is the one whose staged descriptor is the
// descriptor the annotation carries. That is the same identity the
// staged-sources check goes on to assert, so a kit with no matching root
// reports an empty stem and fails there rather than being judged against
// a root belonging to something else.
func ownStem(ctx context.Context, a Artifact, d *spec.Descriptor, stems []string) string {
	if len(stems) == 1 {
		return stems[0]
	}
	for _, stem := range stems {
		raw, ok, err := a.ReadFile(ctx, path.Join(StagedKitRoot, stem, stagedDescriptorName))
		if err != nil || !ok {
			continue
		}
		staged, err := spec.Decode(raw)
		if err != nil {
			continue
		}
		if descriptorsAgree(staged, d) {
			return stem
		}
	}
	return ""
}

func fail(format string, args ...any) []report.Finding {
	return []report.Finding{report.Failf(format, args...)}
}

func skip(format string, args ...any) []report.Finding {
	return []report.Finding{report.Skipf(format, args...)}
}

var checks = []check{
	{
		name:        "descriptor-valid",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			raw := []byte(s.artifact.Annotations()[spec.AnnotationDescriptor])
			if _, err := spec.ValidatePublished(raw, s.descriptor); err != nil {
				return fail("published descriptor is invalid: %v", err)
			}
			return nil
		},
	},
	{
		name:        "schema-version-annotation",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			got := s.artifact.Annotations()[spec.AnnotationSchemaVersion]
			if got != s.descriptor.SchemaVersion {
				return fail("%s is %q, descriptor says %q", spec.AnnotationSchemaVersion, got, s.descriptor.SchemaVersion)
			}
			return nil
		},
	},
	{
		name:        "capabilities-annotation",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			got, present := s.artifact.Annotations()[spec.AnnotationCapabilities]
			want := spec.CapabilityTypes(s.descriptor.Capabilities)
			switch {
			case want == "" && present:
				return fail("%s is present as %q, but the kit requests no capabilities", spec.AnnotationCapabilities, got)
			case want == "":
				return nil
			case got != want:
				return fail("%s is %q, descriptor requests %q", spec.AnnotationCapabilities, got, want)
			}
			return nil
		},
	},
	{
		name:        "oci-annotations",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			ann := s.artifact.Annotations()
			derived := spec.OCIAnnotations(s.descriptor)
			var findings []report.Finding
			// Every managed key, not only the derived ones: publishing
			// emits no key for an empty field, so an annotation the
			// descriptor cannot account for is as wrong as a mismatched
			// one.
			for _, key := range managedOCIKeys {
				want, derivedHas := derived[key]
				got, present := ann[key]
				switch {
				case derivedHas && got != want:
					findings = append(findings, report.Failf("%s is %q, descriptor derives %q", key, got, want))
				case !derivedHas && present:
					findings = append(findings, report.Failf(
						"%s is %q, but the descriptor field it derives from is empty", key, got))
				}
			}
			// Deliberately not emitted: a wall-clock stamp breaks
			// reproducibility, and VCS or base-image state is the
			// builder's knowledge (provenance records it), never the
			// descriptor's.
			for _, key := range []string{
				"org.opencontainers.image.created",
				"org.opencontainers.image.revision",
				"org.opencontainers.image.base.name",
				"org.opencontainers.image.base.digest",
			} {
				if _, present := ann[key]; present {
					findings = append(findings, report.Finding{
						Severity: report.Fail,
						Detail:   fmt.Sprintf("%s must not be emitted", key),
					})
				}
			}
			return findings
		},
	},
	{
		name:        "at-least-one-layer",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			layers, ok, err := s.artifact.Layers(ctx)
			if err != nil {
				return fail("read layers: %v", err)
			}
			if !ok {
				return skip("layers are not assembled yet")
			}
			if len(layers) == 0 {
				return fail("manifest has no layers; the OCI image-manifest schema requires at least one")
			}
			return nil
		},
	},
	{
		name:        "staged-sources",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			if s.stem == "" {
				return fail("no kit sources staged under %s; every kit is self-describing", StagedKitRoot)
			}
			staged, ok, err := s.artifact.ReadFile(ctx, path.Join(StagedKitRoot, s.stem, stagedDescriptorName))
			if err != nil {
				return fail("read staged descriptor: %v", err)
			}
			if !ok {
				return fail("staged %s is missing under %s", stagedDescriptorName, path.Join(StagedKitRoot, s.stem))
			}
			// The annotation carries compact JSON and the staged file the
			// expanded YAML: two serializations of one document, so they
			// are compared decoded.
			stagedDescriptor, err := spec.Decode(staged)
			if err != nil {
				return fail("staged %s does not decode: %v", stagedDescriptorName, err)
			}
			if !descriptorsAgree(stagedDescriptor, s.descriptor) {
				return fail("staged %s and the descriptor annotation describe different kits", stagedDescriptorName)
			}
			return nil
		},
	},
	{
		name:        "staged-recipe",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			if s.stem == "" {
				return skip("no staged kit root to look in")
			}
			present, err := hasFile(ctx, s.artifact, path.Join(StagedKitRoot, s.stem, stagedRecipeName))
			if err != nil {
				return fail("read staged recipe: %v", err)
			}
			// A declaration-only mixin has no recipe to stage; anything
			// with content does.
			hasRecipe := s.descriptor.Build != "" || s.descriptor.Dockerfile != ""
			if hasRecipe && !present {
				return fail("descriptor declares a recipe but %s is not staged", stagedRecipeName)
			}
			return nil
		},
	},
	{
		name:        "agent-context-staged",
		requirement: "agent-context@1",
		run: func(ctx context.Context, s *state) []report.Finding {
			ac, err := spec.AgentContextOf(s.descriptor.Capabilities)
			if err != nil || ac == nil || ac.ContentFile == "" {
				return nil
			}
			if s.stem == "" {
				return skip("no staged kit root to resolve the context against")
			}
			// Beside this kit's own sources, not merely somewhere under
			// the root: pointing at another kit's directory would make the
			// body something this artifact does not carry.
			own := path.Join(StagedKitRoot, s.stem) + "/"
			clean := path.Clean(ac.ContentFile)
			if !strings.HasPrefix(clean, own) || clean != ac.ContentFile {
				return fail("published contentFile is %q; publishing rewrites it to a path directly under %s", ac.ContentFile, own)
			}
			// The staged descriptor and recipe paths are reserved: a
			// contentFile claiming one would collide with what staging
			// itself writes, and the file existing proves the collision
			// rather than the context. Only the two direct source paths
			// collide — a nested docs/kit.yaml is an ordinary name. The
			// frontend refuses this at build; the shared rule has to
			// refuse it for artifacts the frontend never saw.
			if clean == own+stagedDescriptorName || clean == own+stagedRecipeName {
				return fail("contentFile %q is reserved for staged kit sources", clean)
			}
			present, err := hasFile(ctx, s.artifact, ac.ContentFile)
			if err != nil {
				return fail("read staged context: %v", err)
			}
			if !present {
				return fail("contentFile points at %q, which the image does not carry", ac.ContentFile)
			}
			return nil
		},
	},
	{
		name:        "image-config",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			// Every kit is an ordinary image with an image config, so the
			// blob must parse for BOTH kinds — returning early for mixins
			// would certify an artifact whose config is malformed JSON.
			cfg, err := s.artifact.Config(ctx)
			if err != nil {
				return fail("read image config: %v", err)
			}
			if s.descriptor.Kind != spec.KindWorkload {
				return nil
			}
			// The effective launch command is entrypoint followed by cmd;
			// a slice holding one empty string satisfies a nil check and
			// then executes an empty path.
			argv := append(append([]string{}, cfg.Config.Entrypoint...), cfg.Config.Cmd...)
			if len(argv) == 0 || argv[0] == "" {
				return fail("a workload runs under a bare docker run, so its config needs a runnable entrypoint or cmd")
			}
			return nil
		},
	},
	{
		name:        "merged-set",
		requirement: "SPEC-v3 §9.5",
		run: func(ctx context.Context, s *state) []report.Finding {
			if len(s.descriptor.Kits) == 0 {
				return nil
			}
			var findings []report.Finding
			// A set's content is the kits it lists, so the merged
			// artifact must not also claim a recipe: whichever it was
			// built from, the other is a description of content it
			// does not carry.
			if s.descriptor.Build != "" || s.descriptor.Dockerfile != "" {
				findings = append(findings, report.Failf(
					"descriptor lists kits and declares a content recipe; a kit's content comes from exactly one of them"))
			}
			// Each listed kit's own sources ride along in the merged
			// filesystem, which is what makes a merged kit
			// self-describing about what it was built from — and the
			// only place a consumer can read their declarations
			// without fetching them. Every one of them stages under
			// its own stem (two sharing one is refused at build), so a
			// merged kit carries at least its own root plus theirs.
			stems, err := s.artifact.StagedStems(ctx)
			if err != nil {
				return append(findings, report.Failf("list staged kit roots: %v", err))
			}
			if want := len(s.descriptor.Kits) + 1; len(stems) < want {
				findings = append(findings, report.Failf(
					"descriptor lists %d kits, so at least %d staged roots are expected (theirs plus its own), but %d are present: %s",
					len(s.descriptor.Kits), want, len(stems), strings.Join(stems, ", ")))
			}
			return findings
		},
	},
	{
		name:        "merged-set-declarations",
		requirement: "SPEC-v3 §9.5",
		run: func(ctx context.Context, s *state) []report.Finding {
			if len(s.descriptor.Kits) == 0 {
				return nil
			}
			resolver, ok := s.artifact.(kitResolver)
			if !ok {
				return skip("this source cannot reach the kits the set lists")
			}

			// Each listed kit is read at the digest the set recorded,
			// so what the merge is judged against is what it merged.
			contributions := make([]spec.Contribution, 0, len(s.descriptor.Kits))
			published := make([]spec.Contribution, 0, len(s.descriptor.Kits))
			for _, k := range s.descriptor.Kits {
				listed, err := resolver.ResolveKit(ctx, k.Ref, k.Digest)
				if err != nil {
					return skip("%s is not reachable from here: %v", k.Ref, err)
				}
				raw := listed.Annotations()[spec.AnnotationDescriptor]
				if raw == "" {
					return fail("%s carries no descriptor annotation, so it is not a kit this set could have merged", k.Ref)
				}
				d, err := spec.Decode([]byte(raw))
				if err != nil {
					return fail("%s: descriptor does not decode: %v", k.Ref, err)
				}
				// Held to the published form before it is compared
				// against: a kit the frontend could never have merged
				// — an unmerged set, an unversioned provide — would
				// otherwise have its malformed declarations skipped
				// and the merge reported as conforming to them.
				if _, err := spec.ValidatePublished([]byte(raw), d); err != nil {
					return fail("%s is not a valid published kit, so this set could not have merged it: %v", k.Ref, err)
				}
				published = append(published, spec.Contribution{Reference: k.Ref, Descriptor: d})
				// What the merge read is not this descriptor but its
				// effective form: expanded with the args the set
				// recorded, and versioned by the reference the set
				// named rather than by a version: the kit may have
				// outgrown. Comparing the published form instead
				// reports a conforming set as wrong.
				effective, err := effectiveContribution(k, d)
				if err != nil {
					return fail("%s: %v", k.Ref, err)
				}
				contributions = append(contributions, spec.Contribution{Reference: k.Ref, Descriptor: effective})
			}
			findings := judgeStagedSources(ctx, s.artifact, published)
			findings = append(findings, judgeReExports(s.descriptor, published)...)
			return append(findings, judgeMergedDeclarations(s.descriptor, contributions)...)
		},
	},
	{
		name:        "index-annotations",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			indexAnn, ok := s.artifact.IndexAnnotations()
			if !ok {
				return nil
			}
			manifestAnn := s.artifact.Annotations()
			var findings []report.Finding
			// Absence is an optimization, never the contract: a
			// multi-node builder merges results into a fresh index and
			// dissolves annotations, and consumers must fall back to the
			// manifest. Every promoted key gets the same treatment —
			// warning when the manifest carries it and the index does
			// not, since consumers reading only the index would take a
			// missing capabilities value as "none requested". Present but
			// disagreeing is worse than absent: a consumer reading the
			// index would judge a different kit than the one it runs.
			for _, key := range []string{
				spec.AnnotationDescriptor,
				spec.AnnotationSchemaVersion,
				spec.AnnotationCapabilities,
			} {
				manifestValue, onManifest := manifestAnn[key]
				value, onIndex := indexAnn[key]
				switch {
				case onIndex && !onManifest:
					// Presence is meaning: an empty capabilities value
					// says "none requested", which an index must not say
					// when the manifest says nothing at all.
					findings = append(findings, report.Failf(
						"index carries %s, which the manifest does not; the index describes a kit the manifest is not", key))
				case !onIndex && onManifest:
					findings = append(findings, report.Warnf(
						"index carries no %s annotation; consumers fall back to the platform manifest", key))
				case onIndex && value != manifestValue:
					findings = append(findings, report.Failf(
						"index %s disagrees with the manifest's", key))
				}
			}
			return findings
		},
	},
}

// kitResolver is a source that can reach the kits a merged set lists,
// which is what makes the merge checkable against its inputs rather
// than only against itself. A source with one artifact and no registry
// behind it does not implement this, and the check skips.
type kitResolver interface {
	ResolveKit(ctx context.Context, ref, digest string) (Artifact, error)
}

// judgeReExports holds the set's arg declarations to the contracts of
// the kits' args they stand in for.
//
// Checkable from the artifact alone, which is why it is checked here:
// the merged descriptor keeps kits[].args and the set's own args, and
// each kit's declarations come from the digest it is pinned to. What
// the rule protects is the installer, who sees only the set's
// declaration — so an enum re-exported through an unconstrained arg
// puts a value the kit would have refused in front of it with
// nothing left to refuse.
func judgeReExports(merged *spec.Descriptor, kits []spec.Contribution) []report.Finding {
	byReference := make(map[string]*spec.Descriptor, len(kits))
	for _, c := range kits {
		byReference[c.Reference] = c.Descriptor
	}

	var findings []report.Finding
	for _, k := range merged.Kits {
		d, ok := byReference[k.Ref]
		if !ok {
			continue
		}
		names := make([]string, 0, len(k.Args))
		for name := range k.Args {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := k.Args[name]
			if !spec.IsWholeArgRef(value) {
				continue
			}
			referenced := spec.ReferencedArgs([]byte(value))
			if len(referenced) != 1 {
				continue
			}
			setArg, declared := merged.Args[referenced[0]]
			if err := spec.CheckReExport(name, d.Args[name], referenced[0], setArg, declared); err != nil {
				findings = append(findings, report.Failf("%s: %v", k.Ref, err))
			}
		}
	}
	return findings
}

// judgeStagedSources binds each listed kit to a staged root carrying
// its descriptor.
//
// The count alone proves nothing: an artifact with as many unrelated
// roots as it lists kits satisfies it while carrying none of their
// sources. What §10 asks is that a consumer can read the declarations
// of every kit that went in, so each one has to be findable — matched
// by the descriptor itself, since a staged root is named for a
// filename stem the reference does not carry.
func judgeStagedSources(ctx context.Context, a Artifact, kits []spec.Contribution) []report.Finding {
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return fail("list staged kit roots: %v", err)
	}
	staged := make([]*spec.Descriptor, 0, len(stems))
	for _, stem := range stems {
		raw, ok, err := a.ReadFile(ctx, path.Join(StagedKitRoot, stem, stagedDescriptorName))
		if err != nil || !ok {
			continue
		}
		if d, err := spec.Decode(raw); err == nil {
			staged = append(staged, d)
		}
	}

	// Consumed as they match: two kits may publish identical
	// descriptors, and one staged root standing in for both would let
	// an artifact carry half of what it lists.
	used := make([]bool, len(staged))
	var findings []report.Finding
	for _, c := range kits {
		found := false
		for i, d := range staged {
			if !used[i] && descriptorsAgree(d, c.Descriptor) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			findings = append(findings, report.Failf(
				"%s is listed but its staged sources are not present; a merged kit carries the declarations of every kit it merged, and %d staged root(s) hold something else",
				c.Reference, len(staged)))
		}
	}
	return findings
}

// judgeMergedDeclarations holds a merged descriptor to the kits it says
// it was merged from.
//
// Three of §9.5's rules are visible from outside: a listed kit's
// provides carry over, its licenses carry over, and a requirement the
// set answers internally is dropped while one nothing answers is kept.
// The rest of the merge — how policies union, how hooks order — cannot
// be recovered from the result, because the merged entry does not say
// which contribution each part came from.
//
// Only the subset direction is judged. The merged descriptor also
// carries the set's OWN declarations, which no consumer can tell apart
// from its kits', so "nothing was lost" is checkable and "nothing was
// invented" is not.
func judgeMergedDeclarations(merged *spec.Descriptor, kits []spec.Contribution) []report.Finding {
	var findings []report.Finding

	carried := map[string]bool{}
	for _, s := range merged.Provides {
		p, err := spec.ParseProvide(s)
		if err != nil {
			continue
		}
		// A published descriptor may leave a provide bare and carry
		// the version in version:, which §9.2 permits — so the key
		// has to materialize the merged descriptor's own fallback,
		// or a conforming set reads as having dropped what it kept.
		if p.Version == "" {
			p.Version = merged.Version
		}
		carried[p.Name+"@"+p.Version] = true
	}
	for _, c := range kits {
		for _, s := range c.Descriptor.Provides {
			p, err := spec.ParseProvide(s)
			if err != nil {
				continue
			}
			if p.Version == "" {
				p.Version = resolve.EffectiveProvideVersion(c.Reference, c.Descriptor)
			}
			if !carried[p.Name+"@"+p.Version] {
				findings = append(findings, report.Failf(
					"%s provides %s@%s, which the merged descriptor does not; a merged kit offers what its kits offered",
					c.Reference, spec.DisplayCapabilityName(p.Name), p.Version))
			}
		}
	}

	licensed := map[string]bool{}
	for _, l := range merged.Licenses {
		licensed[l] = true
	}
	for _, c := range kits {
		for _, l := range c.Descriptor.Licenses {
			if !licensed[l] {
				findings = append(findings, report.Failf(
					"%s is licensed %s, which the merged descriptor does not name; a merged kit ships its kits' content and reports their terms",
					c.Reference, l))
			}
		}
	}

	// A requirement the set answers itself is gone from the merged
	// descriptor; one nothing in the set answers is still there, since
	// it is now an ask of whatever composition the merged kit lands in.
	// Ownership, as the merge and the resolver both apply it: a kit's
	// own provide never answers its own requirement, so a merged
	// descriptor that dropped one on that basis is wrong however it
	// reads in isolation.
	owned := spec.OwnedProvides(kits)
	for _, relation := range []struct {
		name         string
		mergedStates []string
		of           func(*spec.Descriptor) []string
	}{
		{"requires", merged.Requires, func(d *spec.Descriptor) []string { return d.Requires }},
		{"integrates", merged.Integrates, func(d *spec.Descriptor) []string { return d.Integrates }},
	} {
		// Indexed by what each entry means rather than how it was
		// typed: an artifact restating an input in an equivalent
		// spelling would otherwise read as having dropped it.
		stated := map[string]bool{}
		for _, s := range relation.mergedStates {
			if r, err := spec.ParseRequire(s); err == nil {
				stated[spec.CanonicalRequire(r)] = true
			}
		}
		for _, c := range kits {
			for _, s := range relation.of(c.Descriptor) {
				r, err := spec.ParseRequire(s)
				if err != nil {
					continue
				}
				// Only the retained direction is decidable from
				// outside. A dropped entry may have been answered by
				// the set's OWN declarations, which the merged
				// descriptor does not distinguish from its kits' —
				// so absence is not evidence of anything, while an
				// entry the listed kits answer and the merge kept is
				// one nothing can ever satisfy.
				if spec.SatisfiedByOther(owned, r, c.Reference) && stated[spec.CanonicalRequire(r)] {
					findings = append(findings, report.Failf(
						"the merged descriptor still %s %q, which the kits it lists answer; a kit cannot satisfy its own requirement, so this can never resolve", relation.name, s))
				}
			}
		}
	}
	return findings
}

// effectiveContribution rebuilds what the frontend merged from one
// listed kit: its published descriptor with the args the set recorded
// resolved into it, and the version its consumption reference carries.
//
// The set's record is what makes this reproducible — the same args,
// against the same digest — so a check reading the published form
// alone would judge the merge against something it never saw.
func effectiveContribution(k spec.Kit, d *spec.Descriptor) (*spec.Descriptor, error) {
	values, err := spec.KitArgValues(d.Args, k.Args)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	expanded, err := spec.ExpandCreateArgs(raw, d.Args, values)
	if err != nil {
		return nil, err
	}
	out, err := spec.Decode(expanded)
	if err != nil {
		return nil, fmt.Errorf("declarations do not decode once the set's args resolve: %w", err)
	}
	out.Version = resolve.EffectiveProvideVersion(k.Ref, out)
	out.Args = nil
	return out, nil
}

// fileChecker is a source that can establish presence without fetching
// content. Existence checks prefer it: a context body is deliberately
// bulky, and reading it in full to learn it exists would subject it to
// content-read bounds that do not apply to it.
type fileChecker interface {
	HasFile(ctx context.Context, name string) (bool, error)
}

func hasFile(ctx context.Context, a Artifact, name string) (bool, error) {
	if c, ok := a.(fileChecker); ok {
		return c.HasFile(ctx, name)
	}
	_, present, err := a.ReadFile(ctx, name)
	return present, err
}

// Requirements lists every requirement id the kit suite's checks name.
func Requirements() []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.requirement)
	}
	return out
}

// CheckNames lists the kit suite's check names, each unique. The
// statement-coverage guard pins its kit mapping to the CHECK, because
// several checks share one section-level requirement id and a mapping
// keyed by section could survive the deletion of the one check that
// actually supplies the evidence.
func CheckNames() []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.name)
	}
	return out
}

// descriptorsAgree reports whether two decoded descriptors describe the
// same kit. Serialization differs between the annotation and the staged
// file — compact JSON against expanded YAML — so they are compared as
// documents rather than as bytes: re-encoded canonically, then equal or
// not. Comparing a hand-picked subset would let a staged descriptor
// differ in everything unlisted.
func descriptorsAgree(a, b *spec.Descriptor) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

// ErrNotAKit reports an artifact carrying no descriptor annotation.
var ErrNotAKit = errors.New("not a kit")

// managedOCIKeys are the org.opencontainers.image.* keys publishing owns.
// created and revision are deliberately never emitted: a wall-clock stamp
// breaks reproducibility, and VCS state is the builder's knowledge.
var managedOCIKeys = []string{
	spec.OCIAnnotationTitle,
	spec.OCIAnnotationDescription,
	spec.OCIAnnotationAuthors,
	spec.OCIAnnotationSource,
	spec.OCIAnnotationLicenses,
	spec.OCIAnnotationVersion,
}
