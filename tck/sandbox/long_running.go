package sandbox

import (
	"context"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capLongRunning     = "com.docker.sandbox/long-running@1"
	fixtureLongRunning = "long-running"
)

func optionalLongRunning(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandbox(ctx, []string{"long-running-optional"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("an optional long-running entry prevented create: %v", err)}
	}
	return nil
}

func survivesSessionDisconnect(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureLongRunning}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer cleanup()

	before, f := execOutput(ctx, e, id, "kit-tck-background", "start")
	if f != nil {
		return []report.Finding{*f}
	}
	if strings.TrimSpace(before) == "" {
		return []report.Finding{report.Failf("background process returned no identity")}
	}
	if err := e.Adapter.WaitIdle(ctx, id); err != nil {
		return []report.Finding{report.Failf("wait for final-session disconnection: %v", err)}
	}
	state, err := e.Adapter.Status(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("status after disconnection: %v", err)}
	}
	if state != "running" {
		return []report.Finding{report.Failf("sandbox stopped after its last session disconnected")}
	}
	// Identity comes from a live process responding to a fresh challenge,
	// not a stale marker that would survive a stop and restart.
	after, f := execOutput(ctx, e, id, "kit-tck-background", "probe")
	if f != nil {
		return []report.Finding{*f}
	}
	if after != before {
		return []report.Finding{report.Failf("background process changed across session disconnection")}
	}
	return nil
}

func longRunningStop(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureLongRunning}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer cleanup()
	state, err := e.Adapter.Status(ctx, id)
	if err != nil || state != "running" {
		return []report.Finding{report.Failf("sandbox must be running before stop: state %q, error %v", state, err)}
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	state, err = e.Adapter.Status(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("status after stop: %v", err)}
	}
	if state != "stopped" {
		return []report.Finding{report.Failf("explicit stop left a long-running sandbox %s", state)}
	}
	return nil
}
