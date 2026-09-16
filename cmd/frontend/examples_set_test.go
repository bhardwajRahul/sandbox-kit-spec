package main

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/resolve"
	"github.com/docker/sandbox-kit-spec/spec"
)

// A set example names published kits, so it cannot be built here the
// way every other example can — its content lives in registries. That
// leaves its coherence unchecked by anything: the grammar test decodes
// the descriptor, and the end-to-end test merges kits it synthesizes
// itself, so a set example could list kits that conflict and nothing
// would say so until someone pushed all of them.
//
// This runs the two steps a build would: resolve the listed kits as a
// set, then merge their declarations. The kits are read from the
// examples directory rather than a registry — the references follow the
// sbx-kit-<example> convention the Taskfile pushes under, so the local
// file is the same descriptor the build would resolve, modulo the tag.
func TestExampleSetsResolveAndMerge(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "*.yaml"))
	require.NoError(t, err)

	sets := 0
	for _, descriptor := range matches {
		d := publishedForm(t, descriptor)
		if len(d.Kits) == 0 {
			continue
		}
		sets++
		t.Run(filepath.Base(descriptor), func(t *testing.T) {
			var units []*resolve.Unit
			byUnit := map[*resolve.Unit]spec.Contribution{}
			for _, k := range d.Kits {
				listed := publishedForm(t, exampleFor(t, k.Ref))
				listed.Version = resolve.EffectiveProvideVersion(k.Ref, listed)
				u := &resolve.Unit{Reference: k.Ref, Descriptor: listed}
				units = append(units, u)
				byUnit[u] = spec.Contribution{Reference: k.Ref, Descriptor: listed}
			}

			// The same judgment buildSet makes: one workload at most,
			// every requirement answered inside the set, one provider
			// per name, one credential owner, providers ordered first.
			resolution, err := resolve.ResolvePartial(units)
			require.NoError(t, err, "the kits this set lists do not compose")

			var contributions []spec.Contribution
			for _, u := range resolution.Ordered() {
				contributions = append(contributions, byUnit[u])
			}
			own := *d
			own.Kits = nil
			own.Kind = spec.KindMixin
			require.NoError(t, checkSetRelations(&own, contributions))
			contributions = append(contributions, spec.Contribution{Reference: descriptor, Descriptor: &own})

			result, err := spec.Merge(contributions, spec.MergeOptions{ContextPath: "/staged/context.md"})
			require.NoError(t, err, "the kits this set lists do not merge")

			merged := result.Descriptor
			merged.Version = d.Version
			raw, err := marshalDescriptorYAML(merged)
			require.NoError(t, err)
			_, err = spec.ValidatePublished(raw, merged)
			require.NoError(t, err, "the merged descriptor is not a valid published kit")

			require.Empty(t, merged.Requires,
				"a set example should answer its own requirements; %s leaves %v open", descriptor, merged.Requires)
		})
	}
	require.NotZero(t, sets, "no set examples found; the guard would pass vacuously")
}

// publishedForm reads an example descriptor and resolves its
// build-phase args from their defaults, which is the form a consumer
// sees: provides are literal, and a set's kit references name a
// registry rather than an arg.
func publishedForm(t *testing.T, descriptor string) *spec.Descriptor {
	t.Helper()
	raw, err := os.ReadFile(descriptor)
	require.NoError(t, err)
	d, err := spec.Decode(raw)
	require.NoError(t, err)

	build := map[string]spec.Arg{}
	for name, decl := range d.Args {
		if decl.BuildArg != "" {
			build[name] = decl
		}
	}
	values, err := spec.ResolveArgs(build, nil)
	require.NoError(t, err, "%s: a build-phase arg has no default to expand", descriptor)
	expanded, err := spec.ExpandBuildArgs(raw, d.Args, values)
	require.NoError(t, err)
	out, err := spec.Decode(expanded)
	require.NoError(t, err)
	return out
}

// exampleFor maps a listed kit's reference back to the example that
// publishes it, by the sbx-kit-<example> convention kit:push uses.
func exampleFor(t *testing.T, ref string) string {
	t.Helper()
	name := path.Base(ref)
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "sbx-kit-")
	descriptor := filepath.Join("..", "..", "examples", name, name+".yaml")
	_, err := os.Stat(descriptor)
	require.NoError(t, err, "%s names no example under examples/%s", ref, name)
	return descriptor
}
