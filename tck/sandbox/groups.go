package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const groupVolume = "com.docker.sandbox/volume@1"

func atomicSelection(ctx context.Context, e *Env) []report.Finding {
	lifecycle, volume := e.Claimed[capLifecycle], e.Claimed[groupVolume]
	if lifecycle && volume {
		return groupSelection(ctx, e)
	}
	if !lifecycle && !volume {
		return []report.Finding{report.Skipf("runtime claims neither lifecycle nor volume")}
	}
	if lifecycle {
		if findings := ordinarySelection(ctx, e); len(findings) > 0 {
			return findings
		}
	}
	rejectedMember := "capabilities[0].group.capabilities[1]"
	if lifecycle {
		rejectedMember = "capabilities[0].group.capabilities[0]"
	}
	_, requiredCleanup, requiredErr := e.sandbox(ctx, []string{fixtureWorkload, "groups-required"}, nil)
	requiredCleanup()
	var refused *adapter.RefusedError
	if !errors.As(requiredErr, &refused) || !strings.Contains(refused.Detail, "groups-required") || !strings.Contains(refused.Detail, rejectedMember) {
		return []report.Finding{report.Failf("partially supported required group must refuse and identify %s: %v", rejectedMember, requiredErr)}
	}
	// One member is supported, the other is not: neither may be applied.
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "groups-partial"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("partial group create: %v", err)}
	}
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("partial group selection: %v", err)}
	}
	surface, err := json.Marshal(state.Surface)
	if err != nil || string(surface) != "{}" {
		return []report.Finding{report.Failf("partial group leaked grants: %s (%v)", surface, err)}
	}
	rejected := "capabilities[0].group.capabilities[1]"
	if lifecycle {
		rejected = "capabilities[0].group.capabilities[0]"
	}
	found := false
	for _, record := range state.Selection.Skipped {
		if record.Source != nil && strings.Contains(record.Source.Kit, "groups-partial") && record.Path == "capabilities[0]" {
			found = slices.Equal(record.Rejected, []string{rejected}) && slices.Equal(record.Members,
				[]string{"capabilities[0].group.capabilities[0]", "capabilities[0].group.capabilities[1]"})
		}
	}
	if !found {
		return []report.Finding{report.Failf("partial group rejection record missing or incorrect")}
	}
	file, err := e.Adapter.Exec(ctx, id, "cat", "/var/tmp/group-feature")
	if err != nil || file.ExitCode == 0 {
		return []report.Finding{report.Failf("partial group applied a skipped lifecycle file: %v", err)}
	}
	return nil
}

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
		wantSurface := spec.Surface{}
		if !reject {
			wantSurface.StoragePaths = []string{"/var/tmp/group-volume"}
		}
		actualSurface, err := json.Marshal(state.Surface)
		if err != nil {
			return []report.Finding{report.Failf("encode observed surface: %v", err)}
		}
		expectedSurface, err := json.Marshal(wantSurface)
		if err != nil {
			return []report.Finding{report.Failf("encode expected surface: %v", err)}
		}
		if string(actualSurface) != string(expectedSurface) {
			return []report.Finding{report.Failf("selected permission surface: got %s, want %s", actualSurface, expectedSurface)}
		}
		records := state.Selection.Selected
		if reject {
			records = state.Selection.Skipped
		}
		found := false
		sourceKit := ""
		for _, r := range records {
			if r.Path == "capabilities[1]" {
				found = true
				members := []string{"capabilities[1].group.capabilities[0]", "capabilities[1].group.capabilities[1]"}
				if !slices.Equal(r.Members, members) {
					return []report.Finding{report.Failf("group record lost ordered member locations: got %v, want %v", r.Members, members)}
				}
				if r.Source == nil || r.Source.Path != "capabilities[1]" ||
					(r.Source.Kit != "groups" && r.Source.Kit != e.Fixtures("groups")) {
					return []report.Finding{report.Failf("group record has incorrect source identity: %v", r.Source)}
				}
				sourceKit = r.Source.Kit
				var rejected []string
				if reject {
					rejected = []string{"capabilities[1].group.capabilities[0]"}
				}
				if !slices.Equal(r.Rejected, rejected) {
					return []report.Finding{report.Failf("incorrect rejected members: got %v, want %v", r.Rejected, rejected)}
				}
			}
		}
		if !found {
			return []report.Finding{report.Failf("group selection record missing")}
		}
		for _, expected := range []struct {
			path   string
			member string
		}{
			{"capabilities[0]", "capabilities[0]"},
			{"capabilities[2]", "capabilities[2].group.capabilities[0]"},
		} {
			found := false
			for _, record := range state.Selection.Selected {
				if record.Path == expected.path && record.Source != nil && record.Source.Kit == sourceKit {
					found = record.Source.Path == expected.path &&
						slices.Equal(record.Members, []string{expected.member}) && len(record.Rejected) == 0
				}
			}
			if !found {
				return []report.Finding{report.Failf("required selection record %s missing or incomplete", expected.path)}
			}
		}
	}
	for i, rejected := range []string{groupVolume, capLifecycle} {
		_, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "groups-required"}, adapter.CreateOptions{RejectCapabilities: []string{rejected}})
		cleanup()
		var refused *adapter.RefusedError
		path := fmt.Sprintf("capabilities[0].group.capabilities[%d]", i)
		if !errors.As(err, &refused) || !strings.Contains(refused.Detail, "groups-required") || !strings.Contains(refused.Detail, path) {
			return []report.Finding{report.Failf("required group must refuse and identify rejected member %s: %v", path, err)}
		}
	}
	if findings := groupedLifecycleRejection(ctx, e); len(findings) > 0 {
		return findings
	}
	if findings := ordinarySelection(ctx, e); len(findings) > 0 {
		return findings
	}
	return republishedGroupSelection(ctx, e)
}

// This fixture has no required lifecycle entry outside the optional group,
// so rejecting lifecycle can exercise a successful create with a skipped group.
func groupedLifecycleRejection(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "groups-partial"}, adapter.CreateOptions{RejectCapabilities: []string{capLifecycle}})
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("optional grouped lifecycle rejection: %v", err)}
	}
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("grouped lifecycle selection records: %v", err)}
	}
	found := false
	for _, record := range state.Selection.Skipped {
		if record.Path == "capabilities[0]" && record.Source != nil && strings.Contains(record.Source.Kit, "groups-partial") {
			found = slices.Equal(record.Rejected, []string{"capabilities[0].group.capabilities[1]"})
		}
	}
	surface, err := json.Marshal(state.Surface)
	if !found || err != nil || string(surface) != "{}" {
		return []report.Finding{report.Failf("grouped lifecycle rejection lost skip attribution or leaked grants: %+v", state)}
	}
	file, err := e.Adapter.Exec(ctx, id, "cat", "/var/tmp/group-feature")
	if err != nil || file.ExitCode == 0 {
		return []report.Finding{report.Failf("grouped lifecycle rejection applied a skipped file: %v", err)}
	}
	return nil
}

func republishedGroupSelection(ctx context.Context, e *Env) []report.Finding {
	wantSources := []spec.CapabilitySource{
		{Kit: "registry.example/feature:1.0.0", Path: "capabilities[0].group.capabilities[0]"},
		{Kit: "registry.example/feature:1.0.0", Path: "capabilities[0].group.capabilities[1]"},
	}
	for _, reject := range []bool{false, true} {
		opts := adapter.CreateOptions{}
		if reject {
			opts.RejectCapabilities = []string{groupVolume}
		}
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "groups-republished"}, opts)
		defer cleanup()
		if err != nil {
			return []report.Finding{report.Failf("republished group create: %v", err)}
		}
		state, err := e.Adapter.Selection(ctx, id)
		if err != nil {
			return []report.Finding{report.Failf("republished group selection: %v", err)}
		}
		records := state.Selection.Selected
		var rejected []string
		if reject {
			records = state.Selection.Skipped
			rejected = []string{"capabilities[1].group.capabilities[0]"}
		}
		found := false
		for _, record := range records {
			if record.Path != "capabilities[1]" || record.Source == nil || record.Source.Kit != wantSources[0].Kit {
				continue
			}
			found = true
			if record.Source.Path != "capabilities[0]" || !slices.Equal(record.MemberSources, wantSources) ||
				!slices.Equal(record.Members, []string{"capabilities[1].group.capabilities[0]", "capabilities[1].group.capabilities[1]"}) ||
				!slices.Equal(record.Rejected, rejected) {
				return []report.Finding{report.Failf("republished group lost original member provenance: %+v", record)}
			}
		}
		if !found {
			return []report.Finding{report.Failf("republished group record missing")}
		}
	}
	return nil
}

func ordinarySelection(ctx context.Context, e *Env) []report.Finding {
	for _, reject := range []bool{false, true} {
		opts := adapter.CreateOptions{}
		if reject {
			opts.RejectCapabilities = []string{capLifecycle}
		}
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "ordinary-optional"}, opts)
		defer cleanup()
		if err != nil {
			return []report.Finding{report.Failf("ordinary optional create: %v", err)}
		}
		file, err := e.Adapter.Exec(ctx, id, "cat", "/var/tmp/ordinary-feature")
		if err != nil || (file.ExitCode == 0) == reject {
			return []report.Finding{report.Failf("ordinary optional lifecycle file presence is wrong: %v", err)}
		}
		state, err := e.Adapter.Selection(ctx, id)
		if err != nil {
			return []report.Finding{report.Failf("ordinary selection records: %v", err)}
		}
		records := state.Selection.Selected
		if reject {
			records = state.Selection.Skipped
		}
		found := false
		for _, record := range records {
			if record.Path == "capabilities[0]" && record.Source != nil && strings.Contains(record.Source.Kit, "ordinary-optional") {
				found = record.Source.Path == "capabilities[0]" && slices.Equal(record.Members, []string{"capabilities[0]"})
				if reject {
					found = found && slices.Equal(record.Rejected, []string{"capabilities[0]"})
				} else {
					found = found && len(record.Rejected) == 0
				}
			}
		}
		if !found {
			return []report.Finding{report.Failf("ordinary optional selection/skip record missing or incomplete")}
		}
	}
	_, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "ordinary-required"}, adapter.CreateOptions{RejectCapabilities: []string{capLifecycle}})
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Detail, "ordinary-required") || !strings.Contains(refused.Detail, "capabilities[0]") {
		return []report.Finding{report.Failf("required ordinary rejection must refuse and identify its source: %v", err)}
	}
	return nil
}

// Refused creates have no inspectable sandbox ID. This checks refusal and
// attribution; conflicts-before-application has a separate coverage waiver.
func groupConflicts(ctx context.Context, e *Env) []report.Finding {
	for _, fixture := range []string{"groups-conflict", "groups-interactive-conflict"} {
		_, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
		cleanup()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("%s: selected conflicting groups must refuse: %v", fixture, err)}
		}
		if !strings.Contains(refused.Detail, fixture) || !strings.Contains(refused.Detail, "capabilities[0]") || !strings.Contains(refused.Detail, "capabilities[1]") {
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
	for _, record := range fresh.Selection.Selected {
		if record.Path == "capabilities[1]" {
			return []report.Finding{report.Failf("recreate retained the previously selected group record")}
		}
	}
	found := false
	for _, record := range fresh.Selection.Skipped {
		if record.Path == "capabilities[1]" {
			found = slices.Contains(record.Rejected, "capabilities[1].group.capabilities[0]")
		}
	}
	if !found {
		return []report.Finding{report.Failf("recreate did not record the newly skipped group and rejected member")}
	}
	order, f = execOutput(ctx, e, id, "cat", "/var/tmp/group-order")
	if f != nil {
		return []report.Finding{*f}
	}
	if strings.TrimSpace(order) != "base,independent" {
		return []report.Finding{report.Failf("recreate applied stale group hooks: %q", order)}
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
