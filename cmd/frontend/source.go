package main

import (
	"errors"

	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"

	"github.com/docker/sandbox-kit-spec/spec"
)

// withYAMLSource attaches the descriptor's source to a spec error so
// BuildKit renders the offending YAML lines the way Dockerfile errors
// render theirs. A spec.FieldError narrows the range to the exact element;
// any other error still names the file so the author knows what to open.
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

	var fieldErr *spec.FieldError
	if errors.As(err, &fieldErr) {
		if pos, ok := spec.Positions(raw)[fieldErr.Path]; ok {
			src.Ranges = []*pb.Range{{
				Start: &pb.Position{Line: int32(pos.Line), Character: int32(pos.Column)},
				End:   &pb.Position{Line: int32(pos.EndLine), Character: int32(pos.EndColumn)},
			}}
		}
	}
	return errdefs.WithSource(err, src)
}
