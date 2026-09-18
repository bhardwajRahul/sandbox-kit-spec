package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"path"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/attestations"
	"github.com/moby/buildkit/frontend/dockerui"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

const (
	keyFilename    = "filename"
	keyTarget      = "target"
	keyPlatform    = "platform"
	buildArgPrefix = "build-arg:"

	// stagedKitRoot is where a kit's sources land in the image: the
	// published descriptor as <stem>/kit.yaml, the content recipe as
	// <stem>/kit.dockerfile, and guidance bodies beside them. Every kit
	// is self-describing in its own filesystem — a human or an agent
	// reads the declarations and the recipe in place — and the published
	// descriptor can point into content that travels with it.
	stagedKitRoot = "/usr/share/sandbox/kit"

	stagedDescriptorName = "kit.yaml"
	stagedRecipeName     = "kit.dockerfile"
)

// Build is the gateway entrypoint: one descriptor in, one annotated image
// out — an image index when more than one platform is requested, with the
// descriptor annotation on every platform manifest.
func Build(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
	opts := c.BuildOpts().Opts

	platformList, err := parsePlatforms(opts[keyPlatform])
	if err != nil {
		return nil, err
	}

	filename := opts[keyFilename]
	if filename == "" {
		filename = dockerui.DefaultDockerfileName
	}
	stem := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	companion := path.Join(path.Dir(filename), stem+".dockerfile")

	src, err := readDescriptorSources(ctx, c, filename, companion)
	if err != nil {
		return nil, err
	}

	// The comment-descriptor form (a valid Dockerfile carrying its
	// descriptor in a `# kit:` comment block) is tried only when the file
	// is not a YAML descriptor, so a descriptor that merely mentions the
	// marker in its own comments can never be misrouted. When it matches,
	// the whole untouched file is the content recipe and the stem-found
	// "companion" (the file itself) is discarded.
	var commentContent *companionSource
	d, err := spec.Decode(src.descriptor)
	if err != nil {
		kitYAML, ok := extractCommentDescriptor(src.descriptor)
		if !ok {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
		commentContent = &companionSource{
			name:   path.Base(filename),
			bytes:  contentRecipe(src.descriptor),
			inline: true,
		}
		src = &sources{descriptor: kitYAML}
		if d, err = spec.Decode(kitYAML); err != nil {
			return nil, withYAMLSource(fmt.Errorf("descriptor in %s comment block: %w", kitCommentMarker, err), filename, kitYAML)
		}
	}
	if _, err := spec.ValidateRaw(src.descriptor, d); err != nil {
		return nil, withYAMLSource(err, filename, src.descriptor)
	}

	published, err := expandBuildPhase(src.descriptor, d, opts)
	if err != nil {
		return nil, withYAMLSource(err, filename, src.descriptor)
	}

	content := commentContent
	switch {
	case commentContent != nil:
		if d.Build != "" || d.Dockerfile != "" {
			return nil, fmt.Errorf("%s carries its descriptor in a %s comment block AND declares a build: or dockerfile: recipe; the file itself is already the content recipe", filename, kitCommentMarker)
		}
		if len(d.Kits) > 0 {
			return nil, fmt.Errorf("%s carries its descriptor in a %s comment block AND lists kits:; the file itself is already the content recipe", filename, kitCommentMarker)
		}
	case d.Dockerfile != "":
		content, err = explicitCompanion(ctx, c, d, src, filename, companion)
		if err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
	default:
		content, err = companionSourceFor(d, src, stem, filename, companion)
		if err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
	}
	if content == nil && d.Kind == spec.KindWorkload && len(d.Kits) == 0 {
		return nil, fmt.Errorf("workload kit %s has no content: a workload's layers are its root filesystem, so declare an inline build: block or a companion %s", filename, companion)
	}
	// The grammar refuses build: and dockerfile: beside kits:, but a
	// conventional companion is found by filename rather than declared,
	// so only the frontend can see this one. Left alone it would be
	// staged as the kit's recipe and never built, leaving the artifact
	// describing content it does not carry.
	if len(d.Kits) > 0 && content != nil {
		return nil, fmt.Errorf("%s lists kits: and has a companion %s; a set's content is the kits it lists, so the recipe would be staged but never built — delete the recipe, or drop the kits list", filename, content.name)
	}

	// A set's content is the kits it lists: they are resolved, judged
	// as a set, and merged into one kit before anything else here runs,
	// because the merged descriptor is what gets published — the
	// authored one only says where to find it.
	var plan *setPlan
	if len(d.Kits) > 0 {
		// From the build-expanded form, not the authored one: a set's
		// build-phase args reach its kits through their args: values (a
		// registry namespace, a version they all share), and a
		// reference resolved after the merge would be a reference the
		// published descriptor still carries.
		expanded, err := spec.Decode(published)
		if err != nil {
			return nil, withYAMLSource(fmt.Errorf("descriptor no longer decodes after expansion: %w", err), filename, src.descriptor)
		}
		// Revalidated now that the build-phase values are literal. A
		// capability whose authored config referenced an arg passed
		// leniently — typed checks defer until nothing is a
		// placeholder — and for an ordinary kit the published form is
		// validated later. A set's is not: the merged descriptor
		// replaces it, so this is the only point where what the author
		// actually wrote is judged in full, and setOwnDescriptor can
		// otherwise normalize the evidence away before anything looks.
		if _, err := spec.ValidateRaw(published, expanded); err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
		// The set's OWN declarations, before the merge folds its kits'
		// entries in beside them: afterwards a reserved-namespace entry
		// the author wrote is indistinguishable from one a listed
		// workload legitimately derived.
		if err := spec.RequireAuthoredProvides(expanded); err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
		plan, err = planSet(ctx, c, expanded, platformList, stem)
		if err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
		published = plan.published
	}

	// The agent-context body and the staged path are platform-independent;
	// the staging itself runs per platform against each platform's
	// filesystem.
	agentContext, err := spec.AgentContextOf(d.Capabilities)
	if err != nil {
		return nil, withYAMLSource(err, filename, src.descriptor)
	}
	var guidanceBody []byte
	stagedGuidancePath := ""
	// A set's context is its kits' bodies concatenated with its own,
	// which the merge collected and stageSetContext writes; this is the
	// one-kit case, a single authored body staged as it was written.
	if plan == nil && agentContext != nil && agentContext.ContentFile != "" {
		// The body stages beside the kit's own sources, so a basename
		// matching one of them would replace the descriptor a composed
		// sandbox reads to learn what this kit declared.
		if stagedGuidanceCollides(agentContext.ContentFile) {
			return nil, withYAMLSource(fmt.Errorf(
				"agent-context contentFile %s stages as %s, which is where the kit's own sources go; rename it",
				agentContext.ContentFile, path.Base(agentContext.ContentFile)), filename, src.descriptor)
		}
		guidanceBody, err = readContextFile(ctx, c, strings.TrimPrefix(agentContext.ContentFile, "./"))
		if err != nil {
			return nil, fmt.Errorf("agent-context contentFile %s: %w", agentContext.ContentFile, err)
		}
		stagedGuidancePath = path.Join(stagedKitRoot, stem, path.Base(agentContext.ContentFile))
		published, err = rewriteContentFile(published, agentContext.ContentFile, stagedGuidancePath)
		if err != nil {
			return nil, err
		}
	}

	// Published-form failures point at the authored source: the ranges
	// are computed against the authored bytes, which is what the author
	// can edit.
	pd, err := spec.Decode(published)
	if err != nil {
		return nil, withYAMLSource(fmt.Errorf("published descriptor no longer decodes after expansion: %w", err), filename, src.descriptor)
	}
	// Judged before the derived entries join it, so what an author is
	// told about is what an author wrote. The assembled document is held
	// to the same rules again below.
	if _, err := spec.ValidatePublished(published, pd); err != nil {
		return nil, withYAMLSource(err, filename, src.descriptor)
	}
	// After expansion, and on the expanded form rather than the authored
	// one, because that is where a reserved-namespace entry can actually
	// be seen: an authored `${{ kit.args.cap }}` holds no namespace to
	// refuse until the build-phase value is in it, and ValidatePublished
	// above accepts deb/ and apk/ by design. A set's own declarations are
	// judged before its merge instead, since by here they have its kits'
	// derived entries beside them.
	if plan == nil {
		if err := spec.RequireAuthoredProvides(pd); err != nil {
			return nil, withYAMLSource(err, filename, src.descriptor)
		}
	}

	res := gwclient.NewResult()
	multiPlatform := len(platformList) > 1
	var expPlatforms exptypes.Platforms

	var recipeBytes []byte
	if content != nil {
		recipeBytes = content.bytes
	}

	// Content first, for every platform, because the descriptor is not
	// final until it has been read: §9.6 derives a provides entry per
	// installed package, and the packages are in these filesystems. One
	// descriptor serves every platform — it rides the index as one
	// annotation — so none of it can be settled until all of them exist.
	// The kit's own sources stage in the second pass below, since those
	// are the descriptor.
	builds := make([]platformBuild, 0, len(platformList))
	for _, plat := range platformList {
		b := platformBuild{platform: plat}
		if plan != nil {
			built := plan.builds[platforms.FormatAll(platformOrDefault(plat))]
			b.ref, b.config = built.ref, built.config
			b.ref, err = stageSetContext(ctx, c, b.ref, plat, stagedSetContextPath(stem), built.context)
		} else {
			b.ref, b.config, err = buildPlatform(ctx, c, d, opts, content, plat)
		}
		if err != nil {
			return nil, err
		}
		if guidanceBody != nil {
			b.ref, err = stageGuidance(ctx, c, b.ref, plat, stagedGuidancePath, guidanceBody)
			if err != nil {
				return nil, err
			}
		}
		builds = append(builds, b)
	}

	// Only a workload's filesystem is a root filesystem. A mixin's is a
	// delta, where a package database is whatever its recipe happened to
	// rewrite rather than an inventory of anything.
	//
	// And only a workload BUILT here. A set merges to kind: workload, so
	// its merged descriptor would qualify on kind alone — but §9.6 has a
	// set carry its kits' entries through the provides union rather than
	// re-derive, and deriving again here would append a second copy of
	// every entry the listed workload already contributed, plus whatever
	// packages the mixins' layers happen to have rewritten on top.
	if plan == nil && pd.Kind == spec.KindWorkload {
		refs := make([]gwclient.Reference, 0, len(builds))
		for _, b := range builds {
			refs = append(refs, b.ref)
		}
		derived, err := derivedProvides(ctx, refs)
		if err != nil {
			return nil, err
		}
		if len(derived) > 0 {
			published, pd, err = withDerivedProvides(published, derived)
			if err != nil {
				return nil, withYAMLSource(err, filename, src.descriptor)
			}
		}
	}

	// The annotation carries the published descriptor as compact JSON:
	// authored YAML is the human surface, but the published form is a
	// derived artifact (args expanded, contentFile rewritten), and JSON
	// matches the manifest it rides in — one `jq fromjson` away from any
	// OCI tool, byte-deterministic for a given descriptor. Consumers
	// decode with YAML parsers, which accept JSON as a subset.
	publishedJSON, err := json.Marshal(pd)
	if err != nil {
		return nil, fmt.Errorf("encode published descriptor: %w", err)
	}

	// Assembled once and used twice: the self-check judges the same
	// annotations the exporter writes, so the two cannot disagree.
	kitAnnotations := map[string]string{
		spec.AnnotationDescriptor:    string(publishedJSON),
		spec.AnnotationSchemaVersion: pd.SchemaVersion,
	}
	if capabilityTypes := spec.CapabilityTypes(pd.Capabilities); capabilityTypes != "" {
		kitAnnotations[spec.AnnotationCapabilities] = capabilityTypes
	}
	for key, value := range spec.OCIAnnotations(pd) {
		kitAnnotations[key] = value
	}

	for _, b := range builds {
		ref, err := stageKitSources(ctx, c, b.ref, b.platform, stem, published, recipeBytes)
		if err != nil {
			return nil, err
		}
		if err := selfCheck(ctx, ref, publishedJSON, b.config, kitAnnotations); err != nil {
			return nil, err
		}

		if !multiPlatform {
			res.SetRef(ref)
			res.AddMeta(exptypes.ExporterImageConfigKey, b.config)
			continue
		}
		p := platformOrDefault(b.platform)
		id := platforms.FormatAll(p)
		res.AddRef(id, ref)
		res.AddMeta(exptypes.ExporterImageConfigKey+"/"+id, b.config)
		expPlatforms.Platforms = append(expPlatforms.Platforms, exptypes.Platform{ID: id, Platform: p})
	}

	if multiPlatform {
		platformsJSON, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, platformsJSON)
	}

	// A nil-platform manifest annotation applies to every platform's
	// manifest, so one key annotates the whole index. The schema version
	// rides beside the descriptor so tooling dispatches on the grammar
	// version without parsing YAML, and the capability-types index rides
	// there too so existence checks never parse the descriptor.
	// Standard OCI metadata rides along, derived from the descriptor so
	// registry UIs that know nothing about kits still display title,
	// publisher, license, and version.
	for key, value := range kitAnnotations {
		res.AddMeta(exptypes.AnnotationManifestKey(nil, key), []byte(value))
	}
	// The annotations are promoted onto the image index too, so a
	// consumer resolving the tag learns the kit's declarations from the
	// first GET without descending to a platform manifest. Safe to
	// duplicate: the published descriptor is expanded once, before the
	// platform loop, so index and manifests cannot disagree. Gated on
	// whether this export produces an index at all — multi-platform, or
	// any platform count with live attestations — because the exporter
	// hard-errors on index annotations when a single-platform build
	// exports a bare manifest (nothing to annotate).
	if multiPlatform || attestationsRequested(opts) {
		for key, value := range kitAnnotations {
			res.AddMeta(exptypes.AnnotationIndexKey(key), []byte(value))
		}
	}
	return res, nil
}

// attestationsRequested reports whether this build exports attestation
// MANIFESTS — which is what turns a single-platform export into an image
// index, the only shape that can carry index annotations. Two spellings
// mean no manifest will exist: disabled=true (buildx --provenance=false),
// and inline-only=true, which buildx sends for its default min provenance
// on exports that cannot carry attestation manifests — the provenance
// rides inside the image config and the export stays a bare manifest.
func attestationsRequested(opts map[string]string) bool {
	parsed, err := attestations.Parse(opts)
	if err != nil {
		return false
	}
	for _, attrs := range parsed {
		if attrs["disabled"] == "true" || attrs["inline-only"] == "true" {
			continue
		}
		return true
	}
	return false
}

// parsePlatforms splits the requested platform list. An empty request is
// one nil entry: the worker's default platform, with no platform opt
// forwarded so single-platform behavior is exactly what dockerfile.v0
// would do on its own.
func parsePlatforms(opt string) ([]*ocispecs.Platform, error) {
	if opt == "" {
		return []*ocispecs.Platform{nil}, nil
	}
	var out []*ocispecs.Platform
	seen := map[string]bool{}
	for _, part := range strings.Split(opt, ",") {
		p, err := platforms.Parse(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("parse platform %q: %w", part, err)
		}
		p = platforms.Normalize(p)
		id := platforms.FormatAll(p)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, &p)
	}
	return out, nil
}

func platformOrDefault(p *ocispecs.Platform) ocispecs.Platform {
	if p != nil {
		return *p
	}
	return platforms.DefaultSpec()
}

// companionSource is the kit's content recipe from either home: the
// companion Dockerfile found by the filename-stem convention, or the
// descriptor's inline build: block. Name is what dockerfile.v0 reads (and
// names in errors); inline sources are synthesized into a frontend input
// rather than read from the dockerfile local.
type companionSource struct {
	name   string
	bytes  []byte
	inline bool
}

// explicitCompanion resolves a dockerfile:-named recipe. Unlike the
// conventional companion, whose absence means a declaration-only kit, a
// named recipe that cannot be read is an error — an explicit reference
// failing silently would publish a kit without the content its author
// pointed at. The path resolves against the descriptor's directory,
// which is the dockerfile context's root; when it lands on the
// conventional companion the already-read bytes are used as-is.
func explicitCompanion(ctx context.Context, c gwclient.Client, d *spec.Descriptor, src *sources, filename, conventional string) (*companionSource, error) {
	if d.Build != "" {
		return nil, fmt.Errorf("%s declares an inline build: block AND a dockerfile: recipe; a kit's content recipe lives in exactly one place", filename)
	}
	name := path.Join(path.Dir(filename), path.Clean(d.Dockerfile))
	if name == conventional && src.companion != nil {
		return &companionSource{name: name, bytes: src.companion}, nil
	}
	data, err := readDockerfileLocalFile(ctx, c, name)
	if err != nil {
		return nil, fmt.Errorf("dockerfile: %s names a recipe that cannot be read: %w", d.Dockerfile, err)
	}
	return &companionSource{name: name, bytes: data}, nil
}

// companionSourceFor picks the content recipe, refusing the ambiguity of
// both being declared. Nil means a declaration-only kit.
func companionSourceFor(d *spec.Descriptor, src *sources, stem, filename, companion string) (*companionSource, error) {
	switch {
	case d.Build != "" && src.companion != nil:
		return nil, fmt.Errorf("%s declares an inline build: block AND has a companion %s; a kit's content recipe lives in exactly one place", filename, companion)
	case d.Build != "":
		return &companionSource{name: stem + ".build.dockerfile", bytes: []byte(d.Build), inline: true}, nil
	case src.companion != nil:
		return &companionSource{name: companion, bytes: src.companion}, nil
	default:
		return nil, nil
	}
}

// buildPlatform produces one platform's filesystem and image config: the
// whole content image for workload kits, the content's overlay delta for
// mixins, scratch for declaration-only mixins. Every shape then receives
// the kit's staged sources on top (stageKitSources).
func buildPlatform(ctx context.Context, c gwclient.Client, d *spec.Descriptor, opts map[string]string, content *companionSource, plat *ocispecs.Platform) (gwclient.Reference, []byte, error) {
	switch {
	case content == nil:
		return scratchResult(ctx, c, plat)
	case d.Kind == spec.KindMixin:
		return buildMixinOverlay(ctx, c, d, opts, content, plat)
	default:
		return buildCompanion(ctx, c, d, opts, content, plat)
	}
}

type sources struct {
	descriptor []byte
	companion  []byte // nil when no companion file exists
}

// readDockerfileLocalFile reads one file from the dockerfile local — the
// directory the -f file lives in — for recipes named explicitly rather
// than found by the stem convention.
func readDockerfileLocalFile(ctx context.Context, c gwclient.Client, filename string) ([]byte, error) {
	st := llb.Local(dockerui.DefaultLocalNameDockerfile,
		llb.FollowPaths([]string{filename}),
		llb.SessionID(c.BuildOpts().SessionID),
		llb.SharedKeyHint(dockerui.DefaultLocalNameDockerfile),
		llb.WithCustomName("[internal] load kit recipe "+filename),
	)
	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}
	return ref.ReadFile(ctx, gwclient.ReadRequest{Filename: filename})
}

// readDescriptorSources solves the dockerfile local once and reads the
// descriptor plus the companion Dockerfile, which shares the descriptor's
// directory and stem by convention.
func readDescriptorSources(ctx context.Context, c gwclient.Client, filename, companion string) (*sources, error) {
	st := llb.Local(dockerui.DefaultLocalNameDockerfile,
		llb.FollowPaths([]string{filename, companion}),
		llb.SessionID(c.BuildOpts().SessionID),
		llb.SharedKeyHint(dockerui.DefaultLocalNameDockerfile),
		llb.WithCustomName("[internal] load kit descriptor "+filename),
	)
	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}

	descriptor, err := ref.ReadFile(ctx, gwclient.ReadRequest{Filename: filename})
	if err != nil {
		return nil, fmt.Errorf("read kit descriptor %s: %w", filename, err)
	}

	out := &sources{descriptor: descriptor}
	if data, err := ref.ReadFile(ctx, gwclient.ReadRequest{Filename: companion}); err == nil {
		out.companion = data
	}
	return out, nil
}

// expandBuildPhase resolves the build-phase args from --build-arg values
// keyed by the kit arg's own name, validates them against their
// declarations, and bakes the values into the published descriptor. Args
// without buildArg: resolve at create, not here, and pass through untouched.
func expandBuildPhase(raw []byte, d *spec.Descriptor, opts map[string]string) ([]byte, error) {
	supplied := map[string]string{}
	for name, decl := range d.Args {
		if decl.BuildArg == "" {
			continue
		}
		if v, ok := opts[buildArgPrefix+name]; ok {
			supplied[name] = v
		}
	}

	buildDecls := map[string]spec.Arg{}
	for name, decl := range d.Args {
		if decl.BuildArg != "" {
			buildDecls[name] = decl
		}
	}
	values, err := spec.ResolveArgs(buildDecls, supplied)
	if err != nil {
		return nil, err
	}
	return spec.ExpandBuildArgs(raw, d.Args, values)
}

// buildCompanion forwards the content Dockerfile to dockerfile.v0 — or to
// whatever foreign frontend its own syntax line names, which dockerfile.v0
// honors itself. The only rejected directive is one pointing back at this
// frontend, which would recurse.
func buildCompanion(ctx context.Context, c gwclient.Client, d *spec.Descriptor, opts map[string]string, content *companionSource, plat *ocispecs.Platform) (gwclient.Reference, []byte, error) {
	res, err := solveDockerfile(ctx, c, d, opts, content, plat, "")
	if err != nil {
		return nil, nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, nil, err
	}

	imageConfigJSON := res.Metadata[exptypes.ExporterImageConfigKey]
	if imageConfigJSON == nil {
		return nil, nil, fmt.Errorf("companion build returned no image config")
	}

	if d.Kind == spec.KindWorkload {
		var img ocispecs.Image
		if err := json.Unmarshal(imageConfigJSON, &img); err != nil {
			return nil, nil, fmt.Errorf("parse companion image config: %w", err)
		}
		if len(img.Config.Entrypoint) == 0 && len(img.Config.Cmd) == 0 {
			return nil, nil, fmt.Errorf("workload kit image declares no ENTRYPOINT or CMD; the image config is the runtime contract, set it in %s", content.name)
		}
	}
	return ref, imageConfigJSON, nil
}

// solveDockerfile runs one dockerfile.v0 solve of the content recipe,
// optionally stopping at target. Shared by the workload whole-image build
// and both sides of a mixin overlay diff. Inline recipes are synthesized
// into the dockerfile frontend input; file recipes are read from the
// dockerfile local like any -f file.
func solveDockerfile(ctx context.Context, c gwclient.Client, d *spec.Descriptor, opts map[string]string, content *companionSource, plat *ocispecs.Platform, target string) (*gwclient.Result, error) {
	if line, _, _ := strings.Cut(strings.TrimSpace(string(content.bytes)), "\n"); strings.HasPrefix(line, "# syntax=") && (strings.Contains(line, "sandbox-kit") || strings.Contains(line, "sbx-kit")) {
		return nil, fmt.Errorf("content recipe %s names the kit frontend in its syntax directive; the recipe is an ordinary Dockerfile", content.name)
	}

	fo := dockerfileFrontendOpts(d, opts, content.name, plat)
	if target != "" {
		fo[keyTarget] = target
	}
	req := gwclient.SolveRequest{
		Frontend:    "dockerfile.v0",
		FrontendOpt: fo,
	}
	if content.inline {
		def, err := llb.Scratch().
			File(llb.Mkfile(content.name, 0o644, content.bytes),
				llb.WithCustomName("[kit] inline build block")).
			Marshal(ctx)
		if err != nil {
			return nil, err
		}
		req.FrontendInputs = map[string]*pb.Definition{
			dockerui.DefaultLocalNameDockerfile: def.ToPB(),
		}
	}
	res, err := c.Solve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("build kit content %s: %w", content.name, err)
	}
	return res, nil
}

// dockerfileFrontendOpts assembles the option set forwarded to dockerfile.v0:
// passthrough of target/labels/build-args, the companion as the file, one
// platform, and the kit args mapped onto the Dockerfile ARG names they
// declare — the caller passes --build-arg <kitArgName>=<value>; the
// Dockerfile sees <buildArg>=<value> after validation.
func dockerfileFrontendOpts(d *spec.Descriptor, opts map[string]string, companion string, plat *ocispecs.Platform) map[string]string {
	fo := map[string]string{}
	for k, v := range opts {
		switch {
		case k == buildArgPrefix+"BUILDKIT_SYNTAX":
			// Never forwarded: this is how THIS frontend was dispatched
			// when the descriptor's own syntax line cannot resolve (an
			// unpublished frontend served through the kit registry).
			// dockerfile.v0 honors it like a syntax line, so forwarding
			// it would re-dispatch the companion back into this frontend
			// as a descriptor — recursion the syntax-line self-reference
			// guard cannot see.
		case k == keyTarget:
			fo[k] = v
		case strings.HasPrefix(k, "label:"):
			fo[k] = v
		case strings.HasPrefix(k, buildArgPrefix):
			fo[k] = v
		}
	}
	fo[keyFilename] = companion
	if plat != nil {
		fo[keyPlatform] = platforms.FormatAll(*plat)
	}

	for name, decl := range d.Args {
		if decl.BuildArg == "" {
			continue
		}
		if v, ok := opts[buildArgPrefix+name]; ok {
			fo[buildArgPrefix+decl.BuildArg] = v
		} else if decl.Default != nil {
			fo[buildArgPrefix+decl.BuildArg] = *decl.Default
		}
	}
	return fo
}

// scratchResult is the declaration-only mixin's base: empty, before
// stageKitSources adds the descriptor layer that makes the manifest
// schema-valid (the OCI image-manifest schema requires layers).
func scratchResult(ctx context.Context, c gwclient.Client, plat *ocispecs.Platform) (gwclient.Reference, []byte, error) {
	p := platformOrDefault(plat)

	def, err := llb.Scratch().Marshal(ctx, llb.Platform(p))
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, nil, err
	}

	imageConfigJSON, err := minimalImageConfig(plat)
	if err != nil {
		return nil, nil, err
	}
	return ref, imageConfigJSON, nil
}

// stageKitSources writes the kit's own sources into its filesystem under
// stagedKitRoot/<stem>: the published descriptor as kit.yaml and, when the
// kit has a content recipe, its Dockerfile text as kit.dockerfile. Every
// published kit is self-describing — inspectable in place inside any
// sandbox that composes it — and every manifest carries at least one
// layer, which the OCI image-manifest schema requires (a zero-layer
// manifest is off-spec; the OCI empty descriptor is the ARTIFACT pattern
// and would break the ordinary-pullable-image property kits are built on).
func stageKitSources(ctx context.Context, c gwclient.Client, ref gwclient.Reference, plat *ocispecs.Platform, stem string, published, recipe []byte) (gwclient.Reference, error) {
	st, constraints, err := stateToStageOn(ref, plat)
	if err != nil {
		return nil, err
	}
	dir := path.Join(stagedKitRoot, stem)
	action := llb.Mkdir(dir, 0o755, llb.WithParents(true)).
		Mkfile(path.Join(dir, stagedDescriptorName), 0o644, published)
	if len(recipe) > 0 {
		action = action.Mkfile(path.Join(dir, stagedRecipeName), 0o644, recipe)
	}
	st = st.File(action, llb.WithCustomName("[kit] stage sources "+dir))
	def, err := st.Marshal(ctx, constraints...)
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	return res.SingleRef()
}

// stateToStageOn returns the state to add staged files to, and the
// constraints its marshal needs. A declaration-only kit's base solve is
// empty scratch, whose result carries no reference at all, so every
// staging step has to be able to start from scratch itself — and then
// name the platform, which the absent base would otherwise have carried.
func stateToStageOn(ref gwclient.Reference, plat *ocispecs.Platform) (llb.State, []llb.ConstraintsOpt, error) {
	if ref == nil {
		return llb.Scratch(), []llb.ConstraintsOpt{llb.Platform(platformOrDefault(plat))}, nil
	}
	st, err := ref.ToState()
	if err != nil {
		return llb.State{}, nil, err
	}
	return st, nil, nil
}

// stageGuidance copies the guidance body into one platform's filesystem
// under the stable staged path, so the published artifact is self-contained:
// the context file that fed the build is not needed to consume the kit.
func stageGuidance(ctx context.Context, c gwclient.Client, ref gwclient.Reference, plat *ocispecs.Platform, staged string, body []byte) (gwclient.Reference, error) {
	st, constraints, err := stateToStageOn(ref, plat)
	if err != nil {
		return nil, err
	}
	st = st.File(
		llb.Mkdir(path.Dir(staged), 0o755, llb.WithParents(true)).
			Mkfile(staged, 0o644, body),
		llb.WithCustomName("[kit] stage guidance "+staged),
	)
	def, err := st.Marshal(ctx, constraints...)
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	return res.SingleRef()
}

// platformBuild is one platform's solved content: the filesystem its
// recipe produced, and the image config the exporter will write for it.
type platformBuild struct {
	platform *ocispecs.Platform
	ref      gwclient.Reference
	config   []byte
}

// withDerivedProvides adds the entries §9.6 derived to a published
// descriptor, returning the amended bytes beside their decoded form.
//
// Both travel and both must say the same thing: the annotation carries
// the JSON, the layers stage the YAML, and the staged-sources check
// compares the two as documents. The node tree is amended rather than
// re-encoded from the struct so the copy a reader finds in the image
// keeps the author's own comments and ordering.
func withDerivedProvides(published []byte, derived []string) ([]byte, *spec.Descriptor, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(published, &doc); err != nil {
		return nil, nil, fmt.Errorf("reparse published descriptor: %w", err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("published descriptor is not a mapping")
	}

	provides := mappingValue(root, "provides")
	switch {
	case provides == nil:
		// A kit that declared none still carries what its filesystem
		// holds, so the field is created rather than skipped.
		provides = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "provides"}, provides)
	case provides.Kind == yaml.ScalarNode && provides.Tag == "!!null":
		// `provides:` with nothing under it decodes as null, which is an
		// empty list the author happened to spell differently.
		provides.Kind, provides.Tag, provides.Value = yaml.SequenceNode, "!!seq", ""
	case provides.Kind == yaml.AliasNode && provides.Alias != nil && provides.Alias.Kind == yaml.SequenceNode:
		// `provides: *shared` decodes into a perfectly good list, so a
		// build must not fail on it — and appending through the alias
		// would extend the anchor, so a derived package would turn up in
		// whatever else references it. The list is copied onto the field
		// instead, the way the contentFile rewrite below lands on the
		// field node rather than on the anchor.
		copied := *provides.Alias
		copied.Anchor = ""
		copied.Content = append([]*yaml.Node(nil), provides.Alias.Content...)
		*provides = copied
	case provides.Kind == yaml.SequenceNode && provides.Anchor != "" && aliasedElsewhere(root, provides):
		// The other direction: provides carries the anchor and something
		// else aliases it. Copying cannot help — the anchor is defined
		// here, and moving it would leave those aliases pointing at
		// nothing — while appending would add a derived package to every
		// field that shares the list. Neither is this function's call to
		// make, so it says what it cannot do.
		return nil, nil, fmt.Errorf("provides carries the &%s anchor and another field aliases it, so the entries derived from the image cannot be added without adding them there too; write the shared list out where it is used", provides.Anchor)
	case provides.Kind != yaml.SequenceNode:
		return nil, nil, fmt.Errorf("published provides is not a list, so the derived entries have nowhere to go")
	}
	for _, entry := range derived {
		provides.Content = append(provides.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry})
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, fmt.Errorf("re-encode published descriptor: %w", err)
	}
	pd, err := spec.Decode(out)
	if err != nil {
		return nil, nil, fmt.Errorf("published descriptor no longer decodes with derived provides: %w", err)
	}
	// Held to the published rules a second time, because this document is
	// no longer only the author's: the entries came out of a base image
	// nobody here controls, and the size budget in particular is now
	// partly the derivation's doing.
	if _, err := spec.ValidatePublished(out, pd); err != nil {
		return nil, nil, fmt.Errorf("descriptor with %d provides entries derived from image content: %w", len(derived), err)
	}
	return out, pd, nil
}

// aliasedElsewhere reports whether anything in the document references
// target by alias, so a rewrite can tell an anchor that is load-bearing
// from one nobody uses.
func aliasedElsewhere(root, target *yaml.Node) bool {
	if root == nil || root == target {
		return false
	}
	if root.Kind == yaml.AliasNode {
		return root.Alias == target
	}
	for _, child := range root.Content {
		if aliasedElsewhere(child, target) {
			return true
		}
	}
	return false
}

// rewriteContentFile points the published descriptor's contentFile at the
// staged in-image path. The rewrite is structural — the decoded YAML
// node, not the raw text — because the same string can legitimately occur
// earlier in the document (a description mentioning the file), and a
// textual first-occurrence replace would rewrite the prose and leave the
// field pointing at the build context.
func rewriteContentFile(published []byte, contentFile, staged string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(published, &doc); err != nil {
		return nil, fmt.Errorf("reparse published descriptor: %w", err)
	}
	if !rewriteContentFileNode(&doc, contentFile, staged) {
		return nil, fmt.Errorf("could not rewrite guidance contentFile %q in the published descriptor", contentFile)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode published descriptor: %w", err)
	}
	return out, nil
}

func rewriteContentFileNode(n *yaml.Node, contentFile, staged string) bool {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	capabilities := mappingValue(n, "capabilities")
	if capabilities == nil || capabilities.Kind != yaml.SequenceNode {
		return false
	}
	for _, entry := range capabilities.Content {
		typ := mappingValue(entry, "type")
		if typ == nil || typ.Value != spec.CapabilityAgentContext {
			continue
		}
		config := mappingValue(entry, "config")
		if config == nil {
			continue
		}
		field := mappingValue(config, "contentFile")
		if field == nil {
			continue
		}
		// The field may be an alias into an anchor elsewhere in the
		// document; the VALUE is read through it, but the rewrite lands
		// on the field node itself, or it would rewrite the anchor and
		// everything else referencing it.
		resolved := field
		if resolved.Kind == yaml.AliasNode && resolved.Alias != nil {
			resolved = resolved.Alias
		}
		if resolved.Value == contentFile {
			field.SetString(staged)
			field.Alias = nil
			return true
		}
	}
	return false
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// readContextFile reads one file from the build context local. Guidance
// bodies are authored next to the descriptor, and buildx syncs the -f
// directory as the dockerfile local; the descriptor's directory is the
// context in the common `docker buildx build . -f kit.yaml` invocation, so
// context is where a relative path resolves.
func readContextFile(ctx context.Context, c gwclient.Client, filename string) ([]byte, error) {
	st := llb.Local(dockerui.DefaultLocalNameContext,
		llb.FollowPaths([]string{filename}),
		llb.SessionID(c.BuildOpts().SessionID),
		llb.SharedKeyHint(dockerui.DefaultLocalNameContext),
		llb.WithCustomName("[internal] load guidance "+filename),
	)
	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}
	return ref.ReadFile(ctx, gwclient.ReadRequest{Filename: filename})
}
