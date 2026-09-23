package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerui"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// buildMixinOverlay produces a mixin's content: the layers are exactly the
// delta the companion Dockerfile adds on top of its final stage's base,
// never the base itself. Three shapes of final stage:
//
//   - FROM scratch: the author already assembled the bare overlay (COPY
//     --from=build ...); the built result is the delta, exported as-is.
//   - FROM <earlier stage>: the delta is Diff(that stage, final stage),
//     both solved through dockerfile.v0 (the lower via target=).
//   - FROM <image>: the delta is Diff(base image, final stage), with the
//     base digest-pinned at resolve time so the lower side is byte-for-byte
//     the base dockerfile.v0 pulled.
//
// The mixin's exported image config is the delta its recipe explicitly
// stated over its base — never the base's own config. ENV (PATH reduced
// to added elements), LABEL, EXPOSE, and VOLUME merge additively into the
// composed image at assembly; ENTRYPOINT, CMD, USER, and WORKDIR are
// recorded when the recipe set them so a standalone `docker run` of the
// mixin behaves as authored, but assembly ignores them — the workload kit
// anchors the composed runtime contract.
func buildMixinOverlay(ctx context.Context, c gwclient.Client, d *spec.Descriptor, opts map[string]string, content *companionSource, plat *ocispecs.Platform) (gwclient.Reference, []byte, error) {
	fo := dockerfileFrontendOpts(d, opts, content.name, plat)
	final, err := finalStage(content.bytes, fo)
	if err != nil {
		return nil, nil, fmt.Errorf("content recipe %s: %w", content.name, err)
	}

	upperRes, err := solveDockerfile(ctx, c, d, opts, content, plat, "")
	if err != nil {
		return nil, nil, err
	}
	upperRef, err := upperRes.SingleRef()
	if err != nil {
		return nil, nil, err
	}
	var upper ocispecs.Image
	if raw := upperRes.Metadata[exptypes.ExporterImageConfigKey]; raw != nil {
		if err := json.Unmarshal(raw, &upper); err != nil {
			return nil, nil, fmt.Errorf("parse overlay image config: %w", err)
		}
	}

	if strings.EqualFold(final.base, "scratch") {
		configJSON, cfgErr := mixinImageConfig(plat, configDelta(ocispecs.ImageConfig{}, upper.Config))
		return upperRef, configJSON, cfgErr
	}

	upperState, err := upperRef.ToState()
	if err != nil {
		return nil, nil, err
	}
	lowerState, lower, err := lowerFor(ctx, c, d, opts, content, plat, final)
	if err != nil {
		return nil, nil, err
	}

	diff := llb.Diff(lowerState, upperState,
		llb.WithCustomName("[kit] overlay delta for "+content.name))
	def, err := diff.Marshal(ctx)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB(), Evaluate: true})
	if err != nil {
		return nil, nil, fmt.Errorf("compute overlay delta for %s: %w", content.name, err)
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, nil, err
	}
	configJSON, cfgErr := mixinImageConfig(plat, configDelta(lower, upper.Config))
	return ref, configJSON, cfgErr
}

// lowerFor solves the diff's lower side — the final stage's base — and
// returns its image config alongside, the baseline the config delta is
// computed against.
func lowerFor(ctx context.Context, c gwclient.Client, d *spec.Descriptor, opts map[string]string, content *companionSource, plat *ocispecs.Platform, final stageBase) (llb.State, ocispecs.ImageConfig, error) {
	if final.isStage {
		lowerRes, err := solveDockerfile(ctx, c, d, opts, content, plat, final.base)
		if err != nil {
			return llb.State{}, ocispecs.ImageConfig{}, err
		}
		lowerRef, err := lowerRes.SingleRef()
		if err != nil {
			return llb.State{}, ocispecs.ImageConfig{}, err
		}
		var lower ocispecs.Image
		if raw := lowerRes.Metadata[exptypes.ExporterImageConfigKey]; raw != nil {
			if err := json.Unmarshal(raw, &lower); err != nil {
				return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("parse overlay base config: %w", err)
			}
		}
		st, err := lowerRef.ToState()
		return st, lower.Config, err
	}

	p := platformOrDefault(plat)

	// A named context shadows an external base for dockerfile.v0
	// (--build-context deps=local:...), so the upper side may never have
	// pulled final.base at all. The lower side goes through the same
	// lookup, or the delta would be computed against the wrong
	// filesystem. The name "context" is excluded exactly as dockerfile.v0
	// excludes it: it names the main context, never a base.
	if !strings.EqualFold(final.base, "context") {
		bc, err := dockerui.NewClient(c)
		if err != nil {
			return llb.State{}, ocispecs.ImageConfig{}, err
		}
		nc, err := bc.NamedContext(final.base, dockerui.ContextOpt{
			Platform:    &p,
			ResolveMode: opts[keyImageResolveMode],
		})
		if err != nil {
			return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("overlay base %q: %w", final.base, err)
		}
		if nc != nil {
			st, img, err := nc.Load(ctx)
			if err != nil {
				return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("load overlay base %s from its named context: %w", final.base, err)
			}
			var lower ocispecs.ImageConfig
			if img != nil {
				lower = img.Config.ImageConfig
			}
			return *st, lower, nil
		}
	}

	// External base: pin the digest so the lower side is exactly what
	// dockerfile.v0 pulled for the upper side, not whatever the tag points
	// at by the time this second resolve happens.
	named, err := reference.ParseNormalizedNamed(final.base)
	if err != nil {
		return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("overlay base %q: %w", final.base, err)
	}
	baseRef := reference.TagNameOnly(named).String()
	pinned, dgst, configRaw, err := c.ResolveImageConfig(ctx, baseRef, sourceresolver.Opt{
		ImageOpt: &sourceresolver.ResolveImageOpt{
			Platform: &p,
			// --pull reaches a frontend as image-resolve-mode; an
			// explicit resolve that ignored it would pin a stale local
			// copy of the base the upper side just re-pulled.
			ResolveMode: opts[keyImageResolveMode],
		},
	})
	if err != nil {
		return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("resolve overlay base %s: %w", baseRef, err)
	}
	var lower ocispecs.Image
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &lower); err != nil {
			return llb.State{}, ocispecs.ImageConfig{}, fmt.Errorf("parse overlay base %s config: %w", baseRef, err)
		}
	}
	if pinned == "" {
		pinned = baseRef
	}
	if dgst != "" && !strings.Contains(pinned, "@") {
		pinned = pinned + "@" + dgst.String()
	}
	return llb.Image(pinned, llb.Platform(p), llb.WithCustomName("[kit] overlay base "+final.base)), lower.Config, nil
}

// stageBase names the final stage's base: either an earlier stage (isStage)
// or an external image reference.
type stageBase struct {
	base    string
	isStage bool
}

// finalStage parses the companion and identifies what its final stage
// builds FROM, with ARG references in the FROM line expanded from the
// build args dockerfile.v0 will see (meta-ARG defaults overridden by
// frontend build-arg options). The overlay's shape hangs on this one name,
// so an unresolvable reference is an error, not a guess.
func finalStage(companionBytes []byte, frontendOpts map[string]string) (stageBase, error) {
	ast, err := parser.Parse(bytes.NewReader(companionBytes))
	if err != nil {
		return stageBase{}, fmt.Errorf("parse: %w", err)
	}
	stages, metaArgs, err := instructions.Parse(ast.AST, nil)
	if err != nil {
		return stageBase{}, fmt.Errorf("parse stages: %w", err)
	}
	if len(stages) == 0 {
		return stageBase{}, fmt.Errorf("no FROM stage found")
	}

	args := map[string]string{}
	for _, ma := range metaArgs {
		for _, kv := range ma.Args {
			if kv.Value != nil {
				args[kv.Key] = *kv.Value
			}
		}
	}
	for k, v := range frontendOpts {
		if name, ok := strings.CutPrefix(k, buildArgPrefix); ok {
			args[name] = v
		}
	}

	last := stages[len(stages)-1]
	base := os.Expand(last.BaseName, func(name string) string { return args[name] })
	if strings.Contains(base, "$") || base == "" {
		return stageBase{}, fmt.Errorf("final stage FROM %q does not resolve to a literal base", last.BaseName)
	}

	for _, s := range stages[:len(stages)-1] {
		if s.Name != "" && strings.EqualFold(s.Name, base) {
			return stageBase{base: s.Name, isStage: true}, nil
		}
	}
	return stageBase{base: base}, nil
}

// minimalImageConfig is the config of an image that is content only: a
// mixin never carries runtime config, so nothing beyond the platform is
// stated.
func minimalImageConfig(plat *ocispecs.Platform) ([]byte, error) {
	p := platformOrDefault(plat)
	img := ocispecs.Image{Platform: ocispecs.Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant}}
	return json.Marshal(img)
}

// mixinImageConfig is a mixin's exported config: the platform plus what
// its recipe explicitly stated (the config delta over its base). Contract
// fields the recipe set (entrypoint, cmd, user, workdir) are recorded for
// standalone `docker run` of the mixin; the assembler never reads them.
func mixinImageConfig(plat *ocispecs.Platform, delta ocispecs.ImageConfig) ([]byte, error) {
	p := platformOrDefault(plat)
	img := ocispecs.Image{
		Platform: ocispecs.Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant},
		Config:   delta,
	}
	return json.Marshal(img)
}

// configDelta reduces a mixin build's config to what its recipe stated,
// judged by divergence from the base: ENV entries the base lacks (for
// PATH, only the path elements the recipe added — recorded as a bare PATH
// value holding just those elements, which the assembler appends rather
// than substitutes), LABEL keys new or changed, the EXPOSE/VOLUME sets
// minus the base's, and the runtime-contract fields — ENTRYPOINT, CMD,
// USER, WORKDIR, STOPSIGNAL — where the recipe changed them. A
// mixin built FROM a full OS image therefore records only its own
// statements, never the base's environment. The contract fields serve
// standalone `docker run <mixin>` only; assembly ignores them, the
// workload kit anchors the composed contract.
func configDelta(lower, upper ocispecs.ImageConfig) ocispecs.ImageConfig {
	var delta ocispecs.ImageConfig

	if !slices.Equal(upper.Entrypoint, lower.Entrypoint) {
		delta.Entrypoint = upper.Entrypoint
	}
	if !slices.Equal(upper.Cmd, lower.Cmd) {
		delta.Cmd = upper.Cmd
	}
	if upper.User != lower.User {
		delta.User = upper.User
	}
	if upper.WorkingDir != lower.WorkingDir {
		delta.WorkingDir = upper.WorkingDir
	}
	if upper.StopSignal != lower.StopSignal {
		delta.StopSignal = upper.StopSignal
	}

	lowerEnv := envMap(lower.Env)
	for _, kv := range upper.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == "PATH" {
			added := pathElementsNotIn(v, lowerEnv["PATH"])
			if len(added) > 0 {
				delta.Env = append(delta.Env, "PATH="+strings.Join(added, ":"))
			}
			continue
		}
		if lv, exists := lowerEnv[k]; !exists || lv != v {
			delta.Env = append(delta.Env, kv)
		}
	}

	for k, v := range upper.Labels {
		if lv, exists := lower.Labels[k]; !exists || lv != v {
			if delta.Labels == nil {
				delta.Labels = map[string]string{}
			}
			delta.Labels[k] = v
		}
	}
	for k := range upper.ExposedPorts {
		if _, exists := lower.ExposedPorts[k]; !exists {
			if delta.ExposedPorts == nil {
				delta.ExposedPorts = map[string]struct{}{}
			}
			delta.ExposedPorts[k] = struct{}{}
		}
	}
	for k := range upper.Volumes {
		if _, exists := lower.Volumes[k]; !exists {
			if delta.Volumes == nil {
				delta.Volumes = map[string]struct{}{}
			}
			delta.Volumes[k] = struct{}{}
		}
	}
	return delta
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// pathElementsNotIn returns upperPath's elements missing from lowerPath,
// in upperPath order, deduplicated.
func pathElementsNotIn(upperPath, lowerPath string) []string {
	have := map[string]bool{}
	for _, e := range strings.Split(lowerPath, ":") {
		have[e] = true
	}
	var added []string
	for _, e := range strings.Split(upperPath, ":") {
		if e != "" && !have[e] {
			have[e] = true
			added = append(added, e)
		}
	}
	return added
}
