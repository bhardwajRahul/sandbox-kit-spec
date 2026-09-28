package adapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// SelectionState exposes recorded decisions and the actual effective grant
// surface. Adapters translate runtime records; they must not select again.
type SelectionState struct {
	Selection spec.Selection `json:"selection"`
	Surface   spec.Surface   `json:"surface"`
}

func (a *Adapter) Selection(ctx context.Context, id string) (*SelectionState, error) {
	res, err := a.run(ctx, "selection", id)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("selection: exit %d: %s", res.ExitCode, res.Stderr)
	}
	var state SelectionState
	if err := json.Unmarshal([]byte(res.Stdout), &state); err != nil {
		return nil, fmt.Errorf("selection: %w", err)
	}
	return &state, nil
}

// RejectCapabilities changes future selection answers for this sandbox. It
// does not revoke existing grants; restart must retain the original selection.
func (a *Adapter) RejectCapabilities(ctx context.Context, id string, types ...string) error {
	argv := []string{"selection-policy", id}
	for _, typ := range types {
		argv = append(argv, "--reject-capability", typ)
	}
	return a.mustRun(ctx, argv...)
}
