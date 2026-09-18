package main

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// A declaration-only kit's base solve is empty scratch, which produces no
// reference at all, so every staging step has to be able to start from
// scratch itself. stageGuidance instead called ToState on the reference
// unconditionally, so a mixin whose only content was an agent-context
// contentFile took the frontend down before staging it — surfacing as a
// bare "exit code: 2" with no stack.
func TestStateToStageOnStartsFromScratchForADeclarationOnlyKit(t *testing.T) {
	st, constraints, err := stateToStageOn(nil, &ocispecs.Platform{OS: "linux", Architecture: "s390x"})
	require.NoError(t, err)
	require.NotEmpty(t, constraints,
		"with no base to inherit from, the platform has to be named explicitly")

	// The state has to be usable, not merely non-nil: staging marshals it.
	st = st.File(llb.Mkdir("/staged", 0o755, llb.WithParents(true)).
		Mkfile("/staged/context.md", 0o644, []byte("guidance")))
	def, err := st.Marshal(context.Background(), constraints...)
	require.NoError(t, err)
	require.NotEmpty(t, def.ToPB().Def)
}
