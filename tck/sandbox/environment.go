package sandbox

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func finalEnvironmentPrecedence(ctx context.Context, e *Env) []report.Finding {
	// The workload supplies image-greeting. Every case exports arg-greeting,
	// so an override must beat two different earlier values, not coincide
	// with a value a runtime could obtain without applying the override.
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"argument export", nil, "arg-greeting"},
		{"runtime override", map[string]string{"KIT_GREETING": "runtime-greeting"}, "runtime-greeting"},
		{"empty override", map[string]string{"KIT_GREETING": ""}, ""},
	} {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureFiles}, adapter.CreateOptions{
			Args: map[string]string{"greeting": "arg-greeting"}, Env: tc.env,
		})
		if err != nil {
			cleanup()
			return []report.Finding{report.Failf("create for %s: %v", tc.name, err)}
		}
		body, finding := execOutput(ctx, e, id, "cat", "/home/agent/.config/env-written")
		cleanup()
		if finding != nil {
			return []report.Finding{*finding}
		}
		if body != tc.want {
			return []report.Finding{report.Failf("%s: kit.env expansion did not use the final container environment", tc.name)}
		}
	}
	return nil
}
