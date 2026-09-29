package assemble

import (
	"context"
	"fmt"

	"github.com/containerd/platforms"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
)

// ImageLoader loads a platform manifest and config for an image reference.
// The caller owns credentials, platform selection, and the backing store.
// A registry loader should be used with the digest-pinned Unit.Image values
// produced by fetch.Client.Resolve; source Kits may name store-local images.
type ImageLoader func(ctx context.Context, ref string) (Input, error)

// Assemble loads a resolved Kit set and merges its image metadata. The
// workload is always the first layer stack, even when it depends on a mixin.
// It neither reads layers nor checks filesystem collisions: runtimes must
// perform their content checks before launching the assembled image.
func Assemble(ctx context.Context, kits []*resolve.Unit, load ImageLoader) (*Image, error) {
	if load == nil {
		return nil, fmt.Errorf("assemble: no image loader")
	}
	for _, kit := range kits {
		if kit == nil || kit.Descriptor == nil {
			return nil, fmt.Errorf("assemble: kit has no descriptor")
		}
		if kit.Image == "" {
			return nil, fmt.Errorf("assemble: kit %s has no image reference", kit.Reference)
		}
	}
	resolution, err := resolve.Resolve(kits)
	if err != nil {
		return nil, err
	}
	ordered := resolution.Ordered()
	inputs := make([]Input, 0, len(ordered))
	for _, kit := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		input, err := load(ctx, kit.Image)
		if err != nil {
			return nil, fmt.Errorf("assemble %s: %w", kit.Reference, err)
		}
		input.Name = kit.Reference
		if len(inputs) > 0 && !platforms.OnlyStrict(inputs[0].Config.Platform).Match(input.Config.Platform) {
			return nil, fmt.Errorf("assemble: %s is %s but workload %s is %s", kit.Reference,
				platforms.Format(input.Config.Platform), inputs[0].Name, platforms.Format(inputs[0].Config.Platform))
		}
		inputs = append(inputs, input)
	}
	return Merge(inputs[0], inputs[1:])
}
