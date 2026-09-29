package fetch

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
)

// LayerValidator checks the whole Kit set, including cross-Kit effects.
// Assemble calls it once after validating metadata and resolving declarations.
// kits contains selected, dependency-ordered units; loaded maps their original
// references to verified metadata and optional layer readers. Treat both as
// read-only. A validator returns any failure and honors ctx. It may report
// progress synchronously through report when non-nil.
type LayerValidator func(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error

// DefaultLayerValidator is the built-in LayerValidator. It verifies blob digests and
// diff IDs, enforces inventory limits, models safe extraction, and checks
// cross-Kit file collisions in workload-first image order. Every Kit needs a
// LayerLoader. It reports inventory and collision stages and closes all streams.
// Inputs must contain the validated metadata supplied by Assemble.
func DefaultLayerValidator(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error {
	inventories, err := inventoryKits(ctx, kits, loaded, report)
	if err != nil {
		return err
	}
	return progressStep(ctx, report, Progress{Stage: StageCollisions}, func() error {
		return checkFileCollisions(ctx, inventories)
	})
}
