package kit

import (
	"context"
	"fmt"
	"path"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Inspection is what a kit says about itself: the descriptor it was
// published with, and the sources it staged into its own filesystem.
type Inspection struct {
	// Descriptor is the published descriptor exactly as the manifest
	// annotation carries it: compact JSON, and the document of record.
	Descriptor []byte

	// Stem names the kit's own root under StagedKitRoot, empty when no
	// staged root holds this kit's descriptor.
	Stem string

	// Recipe is the staged content recipe, whichever form it was authored
	// in — companion, dockerfile:, inline build:, or comment descriptor.
	// Nil for a declaration-only kit and for a set, which have none.
	Recipe []byte
}

// RecipePath is where the recipe is staged in the kit's filesystem.
func (i *Inspection) RecipePath() string {
	if i.Stem == "" {
		return ""
	}
	return path.Join(StagedKitRoot, i.Stem, stagedRecipeName)
}

// DescriptorPath is where the descriptor is staged in the kit's filesystem.
func (i *Inspection) DescriptorPath() string {
	if i.Stem == "" {
		return ""
	}
	return path.Join(StagedKitRoot, i.Stem, stagedDescriptorName)
}

// Inspect reads a kit's descriptor and recipe without judging either. The
// recipe is looked up under the same staged root the conformance checks
// pick, so in an artifact carrying several kits' sources it is this kit's
// recipe and not one it was built from.
func Inspect(ctx context.Context, a Artifact) (*Inspection, error) {
	raw := a.Annotations()[spec.AnnotationDescriptor]
	if raw == "" {
		return nil, fmt.Errorf("manifest carries no %s annotation: %w", spec.AnnotationDescriptor, ErrNotAKit)
	}
	d, err := spec.Decode([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("descriptor annotation does not decode: %w", err)
	}
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return nil, fmt.Errorf("list staged kit roots: %w", err)
	}
	in := &Inspection{Descriptor: []byte(raw), Stem: ownStem(ctx, a, d, stems)}
	if in.Stem == "" {
		return in, nil
	}
	recipe, ok, err := a.ReadFile(ctx, in.RecipePath())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", in.RecipePath(), err)
	}
	if ok {
		in.Recipe = recipe
	}
	return in, nil
}
