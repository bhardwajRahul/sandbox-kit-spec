package sandbox

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const groupVolume = "com.docker.sandbox/volume@1"

func groupSelection(ctx context.Context, e *Env) []report.Finding {
	for _, reject := range []bool{false, true} {
		opts := adapter.CreateOptions{}
		if reject {
			opts.RejectCapabilities = []string{groupVolume}
		}
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "groups"}, opts)
		if err != nil {
			cleanup()
			return []report.Finding{report.Failf("group create: %v", err)}
		}
		defer cleanup()
		order, f := execOutput(ctx, e, id, "cat", "/var/tmp/group-order")
		if f != nil {
			return []report.Finding{*f}
		}
		want := "base,feature,independent"
		if reject {
			want = "base,independent"
		}
		if strings.TrimSpace(order) != want {
			return []report.Finding{report.Failf("group hook order: got %q, want %q", order, want)}
		}
		file, err := e.Adapter.Exec(ctx, id, []string{"cat", "/var/tmp/group-feature"}...)
		if err != nil {
			return []report.Finding{report.Failf("read group file: %v", err)}
		}
		if (file.ExitCode == 0) == reject {
			return []report.Finding{report.Failf("selected/skipped group file presence is wrong")}
		}
		state, err := e.Adapter.Selection(ctx, id)
		if err != nil {
			return []report.Finding{report.Failf("selection records: %v", err)}
		}
		if slices.Contains(state.Surface.StoragePaths, "/var/tmp/group-volume") == reject {
			return []report.Finding{report.Failf("permission surface includes skipped or omits selected volume")}
		}
		records := state.Selection.Selected
		if reject {
			records = state.Selection.Skipped
		}
		found := false
		for _, r := range records {
			if r.Path == "capabilities[1]" {
				found = true
				members := []string{"capabilities[1].group.capabilities[0]", "capabilities[1].group.capabilities[1]"}
				if !slices.Equal(r.Members, members) {
					return []report.Finding{report.Failf("group record lost ordered member locations: got %v, want %v", r.Members, members)}
				}
				if r.Source == nil || r.Source.Kit == "" || r.Source.Path == "" {
					return []report.Finding{report.Failf("group record lost source identity")}
				}
				if reject && !slices.Contains(r.Rejected, "capabilities[1].group.capabilities[0]") {
					return []report.Finding{report.Failf("skip record lost rejected member")}
				}
			}
		}
		if !found {
			return []report.Finding{report.Failf("group selection record missing")}
		}
	}
	_, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "groups-required"}, adapter.CreateOptions{RejectCapabilities: []string{groupVolume}})
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Detail, "capabilities[0].group.capabilities[0]") {
		return []report.Finding{report.Failf("required group must refuse and identify rejected member: %v", err)}
	}
	return nil
}

func groupConflicts(ctx context.Context, e *Env) []report.Finding {
	for _, fixture := range []string{"groups-conflict", "groups-interactive-conflict"} {
		_, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
		cleanup()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("%s: selected conflicting groups must refuse: %v", fixture, err)}
		}
		if !strings.Contains(refused.Detail, "capabilities[0]") || !strings.Contains(refused.Detail, "capabilities[1]") {
			return []report.Finding{report.Failf("%s: composition conflict must identify both sources: %s", fixture, refused.Detail)}
		}
	}
	return nil
}

func groupLifetime(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "groups"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	before, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("selection: %v", err)}
	}
	if err := e.Adapter.RejectCapabilities(ctx, id, groupVolume); err != nil {
		return []report.Finding{report.Failf("change future selection: %v", err)}
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	if err := e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("start: %v", err)}
	}
	after, err := e.Adapter.Selection(ctx, id)
	if err != nil || !reflect.DeepEqual(before, after) {
		return []report.Finding{report.Failf("restart changed retained selection: %v", err)}
	}
	order, f := execOutput(ctx, e, id, "cat", "/var/tmp/group-order")
	if f != nil {
		return []report.Finding{*f}
	}
	if strings.TrimSpace(order) != "base,feature,independent" {
		return []report.Finding{report.Failf("restart did not apply the retained group")}
	}
	if err := e.Adapter.Recreate(ctx, id); err != nil {
		return []report.Finding{report.Failf("recreate: %v", err)}
	}
	fresh, err := e.Adapter.Selection(ctx, id)
	if err != nil || slices.Contains(fresh.Surface.StoragePaths, "/var/tmp/group-volume") {
		return []report.Finding{report.Failf("recreate did not select afresh: %v", err)}
	}
	return nil
}

func groupExecution(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "groups-failure"}, nil)
	defer cleanup()
	var refused *adapter.RefusedError
	if err == nil || errors.As(err, &refused) {
		return []report.Finding{report.Failf("selected hook failure must remain an execution error, not skip/refusal: %v", err)}
	}
	return nil
}
