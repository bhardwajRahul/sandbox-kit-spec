package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

func TestBundledSkillChecksAndMutations(t *testing.T) {
	for _, c := range bundledSkillChecks {
		t.Run(c.requirement, func(t *testing.T) {
			modes := []string{""}
			for broken, requirements := range mutations {
				for _, r := range requirements {
					if r == c.requirement {
						modes = append(modes, broken)
					}
				}
			}
			require.Greater(t, len(modes), 1)
			for _, broken := range modes {
				t.Run(broken, func(t *testing.T) {
					a := adapter.New(filepath.Join("testdata", "fake-adapter"))
					a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + broken, "KIT_TCK_FAKE_CLAIMS=" + strings.Join([]string{capAgentSkill, capAgentSkillsDirectory, capAgentSkills}, ","), "KIT_TCK_SKILL_NAME=" + SkillName}
					findings := c.run(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
					if broken == "" {
						require.Empty(t, findings)
						return
					}
					failed := false
					for _, f := range findings {
						failed = failed || f.Severity == report.Fail
					}
					require.True(t, failed, "mutation %s passed: %v", broken, findings)
				})
			}
		})
	}
}

// The claim refusal must be the only reason the bundled request cannot run.
// An adapter accepting unclaimed types still refuses a skill with no destination.
func TestBundledSkillRequiredUnclaimedIsRefused(t *testing.T) {
	for _, destination := range []bool{false, true} {
		for _, broken := range []string{"", "accepts-required-unclaimed"} {
			label := "without-destination-claim/" + broken
			claims := capVolume
			if destination {
				label = "with-destination-claim/" + broken
				claims = capAgentSkillsDirectory
			}
			t.Run(label, func(t *testing.T) {
				a := adapter.New(filepath.Join("testdata", "fake-adapter"))
				a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + broken, "KIT_TCK_FAKE_CLAIMS=" + claims}
				e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir), Claimed: map[string]bool{claims: true}}
				var probe *check
				for i := range checks {
					if checks[i].requirement == "conformance.md §2.2/required-unclaimed-refused" {
						probe = &checks[i]
						break
					}
				}
				require.NotNil(t, probe)
				var skillFindings []report.Finding
				for _, f := range probe.run(t.Context(), e) {
					if strings.Contains(f.Detail, capAgentSkill) {
						skillFindings = append(skillFindings, f)
					}
				}
				if !destination {
					require.Len(t, skillFindings, 1, "an unavailable dependency must skip the skill probe explicitly")
					require.Equal(t, report.Skip, skillFindings[0].Severity)
				} else if broken != "" {
					require.Len(t, skillFindings, 1, "accepting the unclaimed skill must be caught independently of other types")
					require.Equal(t, report.Fail, skillFindings[0].Severity)
					require.Contains(t, skillFindings[0].Detail, "was accepted")
				} else {
					require.Empty(t, skillFindings)
				}
			})
		}
	}
}
