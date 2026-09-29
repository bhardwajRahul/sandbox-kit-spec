package sandbox

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func finalEnvironmentPrecedence(ctx context.Context, e *Env) []report.Finding {
	// Keep the image-only fixture free of argument exports: an argument's
	// default would replace the image value even with no create arguments.
	for _, tc := range []struct {
		name    string
		fixture string
		opts    adapter.CreateOptions
		want    string
	}{
		{
			name: "image default", fixture: "files-image-env",
			want: "image-greeting",
		},
		{
			name: "argument export", fixture: fixtureFiles,
			opts: adapter.CreateOptions{Args: map[string]string{"greeting": "arg-greeting"}},
			want: "arg-greeting",
		},
		{
			name: "runtime override", fixture: fixtureFiles,
			opts: adapter.CreateOptions{
				Args: map[string]string{"greeting": "arg-greeting"},
				Env:  map[string]string{"KIT_GREETING": "runtime-greeting"},
			},
			want: "runtime-greeting",
		},
		{
			name: "empty override", fixture: fixtureFiles,
			opts: adapter.CreateOptions{
				Args: map[string]string{"greeting": "arg-greeting"},
				Env:  map[string]string{"KIT_GREETING": ""},
			},
			want: "",
		},
	} {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, tc.fixture}, tc.opts)
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
