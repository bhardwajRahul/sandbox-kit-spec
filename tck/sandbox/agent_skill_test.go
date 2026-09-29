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
