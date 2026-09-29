package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// Run just this capability's observations, so a focused regression does
// not invoke every unrelated sandbox lifecycle in the complete suite.
func TestGitIdentityChecksAndMutations(t *testing.T) {
	for _, c := range gitIdentityChecks {
		t.Run(c.requirement, func(t *testing.T) {
			modes := []string{""}
			for broken, requirements := range mutations {
				for _, requirement := range requirements {
					if requirement == c.requirement {
						modes = append(modes, broken)
					}
				}
			}
			require.Greater(t, len(modes), 1, "each observable duty needs a mutation")
			for _, broken := range modes {
				t.Run(broken, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					a := adapter.New(filepath.Join("testdata", "fake-adapter"))
					a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + broken, "KIT_TCK_FAKE_CLAIMS=" + capGitIdentity + "," + capLifecycle}
					findings := c.run(ctx, &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
					if strings.HasPrefix(broken, "identity-skip-") {
						require.Len(t, findings, 1)
						require.Contains(t, findings[0].Detail, "optional identity skip record incomplete:")
					}
					if broken == "edits-identity-source" {
						require.Len(t, findings, 1)
						original := gitIdentityConfig(gitIdentityName, gitIdentityEmail)
						require.Equal(t, fmt.Sprintf("identity source changed after guest edit: got %q, want %q", original+"changed\n", original), findings[0].Detail)
					}
					if broken == "git-identity-after-launch" {
						// Both required and optional grants must fail on the
						// startup capture even though their exec defaults pass.
						require.Len(t, findings, 2)
						for _, finding := range findings {
							require.Contains(t, finding.Detail, "probe workload-identity:")
						}
					}
					failed := false
					for _, finding := range findings {
						if finding.Severity == report.Fail {
							failed = true
						}
					}
					require.Equal(t, broken != "", failed, "mutation %q: %v", broken, findings)
				})
			}
		})
	}
}

// The fake adapter simulates probe outcomes, so also run the probe with
// real Git in an isolated config to catch shell/serialization mistakes.
func TestGitIdentityProbe(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	probe, err := filepath.Abs(filepath.Join(FixtureDir, "workload", gitIdentityProbe))
	require.NoError(t, err)
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + root,
		"GIT_CONFIG_GLOBAL=" + config, "GIT_CONFIG_NOSYSTEM=1",
	}
	run := func(argv ...string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = root, env
		return cmd.CombinedOutput()
	}
	put := func(content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(config, []byte(content), 0600))
	}
	guestAlias := "\n[alias]\n kit-tck-guest = status\n"
	put("[user]\n name = Image Author\n email = image@example.invalid\n" + guestAlias)
	out, err := run("sh", probe, "absent")
	require.NoError(t, err, "%s", out)
	require.Equal(t, "absent\n", string(out))
	out, err = run("sh", probe, "workload", "true")
	require.NoError(t, err, "%s", out)

	// Source settings are bait: selecting an identity must copy only the
	// pair, not this entire config. Verify the probe detects the latter.
	put(gitIdentityConfig(gitIdentityName, gitIdentityEmail) + guestAlias)
	out, err = run("sh", probe, "only")
	require.Error(t, err, "%s", out)

	for _, key := range []string{"user.name", "user.email"} {
		out, err = run("git", "config", "--global", "--get", key)
		require.NoError(t, err, "%s", out)
		want := gitIdentityName
		if key == "user.email" {
			want = gitIdentityEmail
		}
		require.Equal(t, want+"\n", string(out))
	}
	put(strings.Split(gitIdentityConfig(gitIdentityName, gitIdentityEmail), "[alias]")[0] + guestAlias)
	// Copying just the user section still leaks signing configuration.
	out, err = run("sh", probe, "only")
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "kit-tck-source-signing-key")
	out, err = run("git", "config", "--global", "--unset", "user.signingKey")
	require.NoError(t, err, "%s", out)
	// A late write changes exec defaults but cannot repair what the
	// workload saw when it started.
	out, err = run("sh", probe, "workload-identity")
	require.NoError(t, err, "%s", out)
	require.Equal(t, "Image Author\nimage@example.invalid\n", string(out))
	out, err = run("sh", probe, "workload", "true")
	require.NoError(t, err, "%s", out)
	for _, mode := range []string{"defaults", "only", "local", "edit"} {
		out, err = run("sh", probe, mode)
		require.NoError(t, err, "probe %s: %s", mode, out)
		if mode == "defaults" {
			require.Equal(t, gitIdentityName+"\n"+gitIdentityEmail+"\n", string(out))
		}
	}
	out, err = run("sh", probe, "workload-identity")
	require.NoError(t, err, "%s", out)
	require.Equal(t, gitIdentityName+"\n"+gitIdentityEmail+"\n", string(out))
}

func TestGitIdentityRequiredUnclaimedIsRefused(t *testing.T) {
	for _, broken := range []string{"", "accepts-required-unclaimed"} {
		t.Run(broken, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + broken, "KIT_TCK_FAKE_CLAIMS=" + capLifecycle}
			findings := withGitIdentity(func(file, _ string) []report.Finding {
				_, cleanup, err := (&Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}).sandboxWith(context.Background(), []string{fixtureWorkload, fixtureGitIdentity}, adapter.CreateOptions{GitIdentityConfig: file})
				defer cleanup()
				if broken == "" {
					var refused *adapter.RefusedError
					require.ErrorAs(t, err, &refused)
				} else {
					require.NoError(t, err)
				}
				return nil
			})
			require.Empty(t, findings)
		})
	}
}
