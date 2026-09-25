package sandbox

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// runAgainstFake drives the suite against a runtime that exists only to be
// driven. Without it the suite would ship never having run: a conformance
// harness that has only ever been read cannot be trusted to fail when it
// should.
func runAgainstFake(t *testing.T, broken string) report.Report {
	t.Helper()
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_BROKEN=" + broken,
		"KIT_TCK_FAKE_CLAIMS=",
	}

	rep, err := Run(context.Background(), &Env{
		Adapter:  a,
		Fixtures: Fixtures(FixtureDir),
	})
	require.NoError(t, err, "the suite must run even when the runtime is wrong")
	return rep
}

// The reason unclaimed capabilities skip at all: a runtime implementing
// one capability well is conforming for that claim. Its checks must run
// and pass while everything else skips — not fail because a fixture
// dragged in a required capability the runtime rightly refused.
func TestASingleCapabilityRuntimeIsJudgedOnlyOnItsClaim(t *testing.T) {
	// Host controls must not turn the conforming fake into a broken one.
	t.Setenv("KIT_TCK_FAKE_BROKEN", "refuses-everything")

	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=com.docker.sandbox/volume@1",
		"KIT_TCK_FAKE_BROKEN=",
	}

	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "a partial implementation is conforming for what it claims:\n%s", rep)

	for _, f := range rep.Findings {
		require.NotContains(t, f.Requirement, "volume@1",
			"the claimed capability's checks must run clean, not skip: %s", f)
	}
}

func TestLongRunningNeedsNoHelperCapabilities(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=" + capLongRunning,
		"KIT_TCK_FAKE_BROKEN=",
	}
	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "long-running alone must be testable:\n%s", rep)
	for _, f := range rep.Findings {
		require.NotContains(t, f.Requirement, "long-running",
			"long-running checks must run clean, not skip: %s", f)
	}
}

// The refusal duty covers well-known types too: a runtime omitting a type
// from its claims and then accepting a kit that requires it would have
// that type's checks skipped and pass while under-provisioning.
func TestARequiredUnclaimedTypeMustBeRefused(t *testing.T) {
	t.Parallel()
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=com.docker.sandbox/volume@1",
		"KIT_TCK_FAKE_BROKEN=accepts-required-unclaimed",
	}

	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.Contains(t, failedRequirements(rep), "conformance.md §2.2/required-unclaimed-refused",
		"accepting a kit that requires an unclaimed well-known type must fail:\n%s", rep)
}

// failedRequirements names what the run judged non-conforming.
func failedRequirements(rep report.Report) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Severity == report.Fail {
			out = append(out, f.Requirement)
		}
	}
	return out
}

func TestAConformingRuntimePasses(t *testing.T) {
	// An empty mutation must override a broken mode inherited from the host.
	t.Setenv("KIT_TCK_FAKE_BROKEN", "refuses-everything")

	rep := runAgainstFake(t, "")
	require.False(t, rep.Failed(), "conforming fake reported failures:\n%s", rep)
}

// Each of these breaks one behavior, so a check that never fails would be
// caught here rather than by trusting it.
// mutations maps a way of breaking the fake runtime to the requirements
// that must notice. One behavior can be named by more than one
// requirement — the two network-policy versions state the same duty about
// the host lists — and dropping it has to fail every one of them.
var mutations = map[string][]string{
	"stops-on-disconnect":               {"long-running@1/survives-session-disconnect"},
	"loses-background-on-disconnect":    {"long-running@1/survives-session-disconnect"},
	"restarts-background-on-disconnect": {"long-running@1/survives-session-disconnect"},
	"idle-errors":                       {"long-running@1/survives-session-disconnect"},
	"status-errors":                     {"long-running@1/survives-session-disconnect", "long-running@1/explicit-stop-honored"},
	"status-malformed":                  {"long-running@1/survives-session-disconnect", "long-running@1/explicit-stop-honored"},
	"ignores-explicit-stop":             {"long-running@1/explicit-stop-honored"},
	"refuses-optional-long-running":     {"conformance.md §2.2/optional-long-running-accepted"},
	"install-twice":                     {"lifecycle@1/install-once"},
	"no-startup":                        {"lifecycle@1/startup-every-boot"},
	"ignores-files":                     {"lifecycle@1/files-written"},
	"writes-files-as-root":              {"lifecycle@1/files-written"},
	"writes-files-read-only":            {"lifecycle@1/files-written"},
	"leaks-env":                         {"lifecycle@1/hook-env-restricted"},
	"allows-everything":                 {"network-policy@1/deny-by-default", "network-policy@2/deny-by-default"},
	"ignores-http-method":               {"network-policy@2/http-method-enforced"},
	"ignores-http-path":                 {"network-policy@2/http-path-enforced"},
	"ignores-http-deny":                 {"network-policy@2/http-deny-precedence"},
	"leaves-install-egress-open": {
		"network-policy@1/install-phase-scoped",
		"network-policy@2/install-phase-scoped",
	},
	"leaks-undeclared-env":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-baseline":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-hostname":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-home":                      {"lifecycle@1/hook-env-restricted"},
	"copies-host-pwd":                       {"lifecycle@1/hook-env-restricted"},
	"leaks-secret-elsewhere":                {"credential@1/secret-absent-in-sandbox"},
	"leaks-secret":                          {"credential@1/secret-absent-in-sandbox"},
	"leaks-secret-decorated":                {"credential@1/secret-absent-in-sandbox"},
	"no-credential":                         {"credential@1/secret-absent-in-sandbox"},
	"leaks-inject-only-env":                 {"credential@1/inject-only-no-env"},
	"hides-inject-only-in-existing-env":     {"credential@1/inject-only-no-env"},
	"hides-inject-only-in-token":            {"credential@1/inject-only-no-env"},
	"leaks-plain-inject-only-env":           {"credential@1/inject-only-no-env"},
	"exposes-install-inject-only":           {"credential@1/inject-only-no-env"},
	"hides-install-inject-only-in-declared": {"credential@1/inject-only-no-env"},
	"assumes-uid-1000":                      {"sbx@1/honors-image-user"},
	"runs-image-entrypoint":                 {"sbx@1/entrypoint-not-pid-one"},
	"workspace-at-fixed-path":               {"sbx@1/workspace-at-workdir"},
	"accept-unknown":                        {"SPEC-v3 §7.3/unknown-required-refused"},
	"refuses-everything":                    {"conformance.md §2.1/baseline-create-succeeds"},
	"no-context":                            {"agent-context@1/body-readable"},
	"loses-volume":                          {"volume@1/persists-across-recreate"},
	"loses-volume-on-restart":               {"volume@1/persists-across-recreate"},
	"keeps-writable-layer":                  {"volume@1/persists-across-recreate"},
	"drops-context-body":                    {"agent-context@1/body-readable"},
	"ignores-privileged":                    {"privileged@1/elevation-granted"},
	"ignores-skills":                        {"agent-skills@1/store-mounted-at-declared-path"},
	"empty-skills":                          {"agent-skills@1/store-mounted-at-declared-path"},
	"wrong-skill":                           {"agent-skills@1/store-mounted-at-declared-path"},
	"first-skills-only":                     {"agent-skills@1/store-mounted-at-declared-path"},
	"writable-decoy":                        {"agent-skills@1/readwrite-granted-when-both-allow"},
	"mounts-skills-late":                    {"agent-skills@1/mounted-before-hooks"},
	"narrows-shared-path":                   {"agent-skills@1/shared-path-widest-mode"},
	"ignores-host-readonly":                 {"agent-skills@1/host-readonly-narrows"},
	"ignores-host-off":                      {"agent-skills@1/host-off-refuses-required"},
	"refuses-optional-skills":               {"agent-skills@1/host-off-skips-optional"},
	"mounts-optional-despite-off":           {"agent-skills@1/host-off-skips-optional"},
	"mounts-later-skills-late":              {"agent-skills@1/mounted-before-hooks"},
	"separate-stores":                       {"agent-skills@1/same-store-at-every-path"},
	"ignores-readonly":                      {"agent-skills@1/readonly-default-honored"},
	"ignores-readwrite":                     {"agent-skills@1/readwrite-granted-when-both-allow"},
}

func TestEachCheckFailsWhenItsBehaviorIsAbsent(t *testing.T) {
	// Host claims must not skip the checks these mutations exercise.
	t.Setenv("KIT_TCK_FAKE_CLAIMS", "com.docker.sandbox/volume@1")

	for broken, requirements := range mutations {
		t.Run(broken, func(t *testing.T) {
			t.Parallel()
			rep := runAgainstFake(t, broken)
			failed := failedRequirements(rep)
			for _, requirement := range requirements {
				require.Contains(t, failed, requirement,
					"breaking %q must fail %s:\n%s", broken, requirement, rep)
			}
		})
	}
}

// A partial implementation is conforming for what it claims, so unclaimed
// capabilities are skipped rather than failed — but the refusal
// requirement still applies, because that is the one thing a runtime must
// do about capabilities it lacks.
func TestUnclaimedCapabilitiesAreSkipped(t *testing.T) {
	t.Parallel()
	rep := runAgainstFake(t, "claims-nothing")
	require.False(t, rep.Failed(), "unclaimed capabilities must not fail:\n%s", rep)

	var skipped int
	for _, f := range rep.Findings {
		if f.Severity == report.Skip {
			skipped++
		}
	}
	require.Positive(t, skipped, "capability checks should have been skipped")
}

// The coverage guard compares these against the specification, so every
// check has to name a requirement and name it once.
func TestRequirementsAreUniqueAndNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Requirements() {
		require.NotEmpty(t, r)
		require.False(t, seen[r], "duplicate requirement id %q", r)
		seen[r] = true
	}
}

// A check with no mutation case is a check nobody has seen fail, so the
// mutation map has to keep pace with the suite. resources@1 is excused
// because its page says SHOULD: the check warns, and a warning is not a
// failure to provoke.
func TestEveryCheckHasAMutationCase(t *testing.T) {
	excused := map[string]string{
		"resources@1/limit-applied":                      "the page says SHOULD, so the check warns rather than fails",
		"conformance.md §2.2/required-unclaimed-refused": "needs a claims subset the mutation table cannot express; TestARequiredUnclaimedTypeMustBeRefused drives it",
	}

	covered := map[string]bool{}
	for _, requirements := range mutations {
		for _, requirement := range requirements {
			covered[requirement] = true
		}
	}
	for _, r := range Requirements() {
		if covered[r] || excused[r] != "" {
			continue
		}
		t.Errorf("requirement %q has no mutation case: add one to the fake adapter, or excuse it with a reason", r)
	}
}

// The refusal signal has to be distinguishable from failure, or a runtime
// that cannot create anything would satisfy every requirement that is met
// by refusing. Here the runtime declines the right kit for the right
// reason but reports it as an error, which must not count.
func TestAFailingCreateIsNotMistakenForARefusal(t *testing.T) {
	t.Parallel()
	rep := runAgainstFake(t, "refusal-as-error")
	require.Contains(t, failedRequirements(rep), "SPEC-v3 §7.3/unknown-required-refused",
		"a create that fails for unrelated reasons must not count as a refusal:\n%s", rep)
}
