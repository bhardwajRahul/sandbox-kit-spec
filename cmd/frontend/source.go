package main

import (
	"errors"

	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// withYAMLSource attaches the descriptor's source to a spec error so
// BuildKit renders the offending YAML lines the way Dockerfile errors
// render theirs. Every spec.FieldError contributes a source range. Keep
// the messages plain: BuildKit renders the attached source itself, so
// applying spec.WithSource here would print a second set of excerpts.
func withYAMLSource(err error, filename string, raw []byte) error {
	if err == nil {
		return nil
	}
	src := &errdefs.Source{
		Info: &pb.SourceInfo{
			Data:     raw,
			Filename: filename,
			Language: "yaml",
		},
	}

	positions := spec.Positions(raw)
	var addRanges func(error)
	addRanges = func(err error) {
		var all spec.ValidationErrors
		if errors.As(err, &all) {
			for _, child := range all {
				addRanges(child)
			}
			return
		}
		var fieldErr *spec.FieldError
		if errors.As(err, &fieldErr) {
			if pos, ok := spec.PositionFor(positions, fieldErr.Path); ok {
				src.Ranges = append(src.Ranges, &pb.Range{
					Start: &pb.Position{Line: int32(pos.Line), Character: int32(pos.Column)},
					End:   &pb.Position{Line: int32(pos.EndLine), Character: int32(pos.EndColumn)},
				})
			}
		}
	}
	addRanges(err)
	return errdefs.WithSource(err, src)
}
