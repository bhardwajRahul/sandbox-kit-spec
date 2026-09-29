package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capGitIdentity             = "com.docker.sandbox/git-identity@1"
	fixtureGitIdentity         = "git-identity"
	fixtureGitIdentityOptional = "git-identity-optional"
	fixtureGitIdentityHooks    = "git-identity-hooks"
	gitIdentityName            = `Kit TCK "Author"; $(false)`
	gitIdentityEmail           = "kit-tck-author@example.invalid"
	gitIdentityProbe           = "kit-tck-git-identity"
)

func gitIdentityConfig(name, email string) string {
	// Git's quoted config syntax, not shell syntax: hostile-looking values
	// are data and must survive a runtime's materialization unchanged.
	quote := func(value string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return "[user]\n name = " + quote(name) + "\n email = " + quote(email) + `
 signingKey = kit-tck-source-signing-key
[alias]
 kit-tck-source = !false
[credential]
 helper = kit-tck-source-helper
[http]
 extraHeader = X-Kit-TCK: source-only
[core]
 hooksPath = /kit-tck-source-hooks
[include]
 path = /kit-tck-source-include
[filter "kit-tck-source"]
 clean = kit-tck-source-filter
[gpg]
 program = kit-tck-source-signing-program
`
}

func withGitIdentity(run func(string, string) []report.Finding) []report.Finding {
	dir, err := os.MkdirTemp("", "kit-tck-git-identity-*")
	if err != nil {
		return []report.Finding{report.Failf("prepare runtime identity: %v", err)}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "config")
	original := gitIdentityConfig(gitIdentityName, gitIdentityEmail)
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		return []report.Finding{report.Failf("write runtime identity: %v", err)}
	}
	return run(file, original)
}

func gitIdentityOutput(ctx context.Context, e *Env, id, mode, want string) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, gitIdentityProbe, mode)
	if err != nil {
		return []report.Finding{report.Failf("probe %s: %v", mode, err)}
	}
	if res.ExitCode != 0 || res.Stdout != want {
		return []report.Finding{report.Failf("probe %s: exit %d, stdout %q, stderr %q; want %q", mode, res.ExitCode, res.Stdout, res.Stderr, want)}
	}
	return nil
}

func gitIdentityDefaults(ctx context.Context, e *Env) []report.Finding {
	return withGitIdentity(func(file, _ string) []report.Finding {
		var findings []report.Finding
		for _, fixture := range []string{fixtureGitIdentity, fixtureGitIdentityOptional} {
			id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixture}, adapter.CreateOptions{GitIdentityConfig: file})
			if err != nil {
				return []report.Finding{report.Failf("create %s: %v", fixture, err)}
			}
			findings = append(findings, gitIdentityOutput(ctx, e, id, "defaults", gitIdentityName+"\n"+gitIdentityEmail+"\n")...)
			findings = append(findings, gitIdentityOutput(ctx, e, id, "workload-identity", gitIdentityName+"\n"+gitIdentityEmail+"\n")...)
			cleanup()
		}
		return findings
	})
}

func gitIdentityObservation(mode, want string, fixtures ...string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		return withGitIdentity(func(file, _ string) []report.Finding {
			id, cleanup, err := e.sandboxWith(ctx, append([]string{fixtureWorkload}, fixtures...), adapter.CreateOptions{GitIdentityConfig: file})
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()
			return gitIdentityOutput(ctx, e, id, mode, want)
		})
	}
}

func gitIdentityUnavailable(ctx context.Context, e *Env) []report.Finding {
	return withGitIdentity(func(file, _ string) []report.Finding {
		for _, missing := range []string{"off", "name", "email"} {
			source := "off"
			if missing != "off" {
				name, email := gitIdentityName, gitIdentityEmail
				if missing == "name" {
					name = ""
				} else {
					email = ""
				}
				if err := os.WriteFile(file, []byte(gitIdentityConfig(name, email)), 0600); err != nil {
					return []report.Finding{report.Failf("prepare missing %s: %v", missing, err)}
				}
				source = file
			}
			opts := adapter.CreateOptions{GitIdentityConfig: source}
			id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureGitIdentity}, opts)
			cleanup()
			var refused *adapter.RefusedError
			if !errors.As(err, &refused) {
				return []report.Finding{report.Failf("required identity with %s missing: id=%q err=%v; want refusal", missing, id, err)}
			}
			id, cleanup, err = e.sandboxWith(ctx, []string{fixtureWorkload, fixtureGitIdentityOptional}, opts)
			if err != nil {
				return []report.Finding{report.Failf("optional identity with %s missing: %v", missing, err)}
			}
			findings := gitIdentityOutput(ctx, e, id, "absent", "absent\n")
			findings = append(findings, gitIdentitySkipped(ctx, e, id)...)
			cleanup()
			if len(findings) != 0 {
				return findings
			}
		}
		return nil
	})
}

func gitIdentitySkipped(ctx context.Context, e *Env, id string) []report.Finding {
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("optional identity selection records: %v", err)}
	}
	fixtureRef := e.Fixtures(fixtureGitIdentityOptional)
	for _, record := range state.Selection.Selected {
		if record.Source != nil && (record.Source.Kit == fixtureGitIdentityOptional || record.Source.Kit == fixtureRef) {
			return []report.Finding{report.Failf("unavailable optional identity recorded as selected: %+v", record)}
		}
	}
	found := 0
	for _, record := range state.Selection.Skipped {
		if record.Source == nil || (record.Source.Kit != fixtureGitIdentityOptional && record.Source.Kit != fixtureRef) {
			continue
		}
		if record.Source.Path != "capabilities[0]" || record.Path != "capabilities[0]" ||
			!slices.Equal(record.Members, []string{"capabilities[0]"}) ||
			!slices.Equal(record.Rejected, []string{"capabilities[0]"}) ||
			!slices.Equal(record.MemberSources, []spec.CapabilitySource{*record.Source}) {
			return []report.Finding{report.Failf("optional identity skip record incomplete: %+v", record)}
		}
		found++
	}
	if found != 1 {
		return []report.Finding{report.Failf("optional identity skip record: got %d, want 1", found)}
	}
	return nil
}

func gitIdentityPersistence(ctx context.Context, e *Env) []report.Finding {
	return withGitIdentity(func(file, _ string) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureGitIdentity}, adapter.CreateOptions{GitIdentityConfig: file})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		if err := os.WriteFile(file, []byte(gitIdentityConfig("Changed Identity", "changed@example.invalid")), 0600); err != nil {
			return []report.Finding{report.Failf("change runtime identity: %v", err)}
		}
		fresh, removeFresh, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureGitIdentity}, adapter.CreateOptions{GitIdentityConfig: file})
		if err != nil {
			return []report.Finding{report.Failf("create with changed binding: %v", err)}
		}
		defer removeFresh()
		if findings := gitIdentityOutput(ctx, e, fresh, "defaults", "Changed Identity\nchanged@example.invalid\n"); len(findings) != 0 {
			return findings
		}
		for _, operation := range []struct {
			name string
			run  func(context.Context, string) error
		}{
			{"stop", e.Adapter.Stop}, {"start", e.Adapter.Start}, {"recreate", e.Adapter.Recreate},
		} {
			if err := operation.run(ctx, id); err != nil {
				return []report.Finding{report.Failf("%s: %v", operation.name, err)}
			}
			if operation.name != "stop" {
				if findings := gitIdentityOutput(ctx, e, id, "defaults", gitIdentityName+"\n"+gitIdentityEmail+"\n"); len(findings) != 0 {
					return findings
				}
			}
		}
		return nil
	})
}

func gitIdentitySourceUnchanged(ctx context.Context, e *Env) []report.Finding {
	return withGitIdentity(func(file, original string) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixtureGitIdentity}, adapter.CreateOptions{GitIdentityConfig: file})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		findings := gitIdentityOutput(ctx, e, id, "defaults", gitIdentityName+"\n"+gitIdentityEmail+"\n")
		findings = append(findings, gitIdentityOutput(ctx, e, id, "edit", "attempted\n")...)
		raw, err := os.ReadFile(file)
		if err != nil {
			findings = append(findings, report.Failf("read identity source after guest edit: %v", err))
		} else if string(raw) != original {
			findings = append(findings, report.Failf("identity source changed after guest edit: got %q, want %q", string(raw), original))
		}
		return findings
	})
}

var gitIdentityChecks = []check{
	{requirement: "git-identity@1/global-defaults", capability: capGitIdentity, run: gitIdentityDefaults},
	{requirement: "git-identity@1/before-hooks", capability: capGitIdentity, needs: []string{capLifecycle}, run: gitIdentityObservation("hooks", strings.Repeat(fmt.Sprintf("%s\n%s\n", gitIdentityName, gitIdentityEmail), 2), fixtureGitIdentity, fixtureGitIdentityHooks)},
	{requirement: "git-identity@1/local-precedence", capability: capGitIdentity, run: gitIdentityObservation("local", "local\n", fixtureGitIdentity)},
	{requirement: "git-identity@1/identity-only", capability: capGitIdentity, run: gitIdentityObservation("only", "only\n", fixtureGitIdentity)},
	{requirement: "git-identity@1/source-unchanged", capability: capGitIdentity, run: gitIdentitySourceUnchanged},
	{requirement: "git-identity@1/pinned-selection", capability: capGitIdentity, run: gitIdentityPersistence},
	{requirement: "git-identity@1/absent-without-grant", capability: capGitIdentity, run: gitIdentityObservation("absent", "absent\n")},
	{requirement: "git-identity@1/unavailable-refuses-required", capability: capGitIdentity, run: gitIdentityUnavailable},
}
