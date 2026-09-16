// Package adapter drives a candidate runtime through the conformance
// contract in docs/spec/conformance.md.
//
// The contract is an executable and a handful of verbs rather than a Go
// interface, so a runtime written in any language can be judged — and so
// the suite tests observable behavior rather than an implementation's
// internals.
package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Adapter is a candidate runtime under test.
type Adapter struct {
	// Path is the executable implementing the contract.
	Path string
	// Env is added to the adapter's environment.
	Env []string
	// Dir, when set, is the working directory verbs run in. The suite
	// points it at a sentinel-named path so a runtime copying the host's
	// PWD into a hook carries the sentinel with it.
	Dir string
}

// Result is one adapter invocation's outcome. A non-zero ExitCode is not
// an error by itself: a test asserting that something is refused needs the
// refusal, not a failure to run.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// New returns an adapter rooted at an executable.
func New(path string) *Adapter { return &Adapter{Path: path} }

// Capabilities lists the types the runtime claims to implement. Tests for
// anything absent are skipped, except the one asserting that a required
// unclaimed type is refused.
func (a *Adapter) Capabilities(ctx context.Context) ([]string, error) {
	res, err := a.run(ctx, "capabilities")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("capabilities: exit %d: %s", res.ExitCode, res.Stderr)
	}
	var types []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			types = append(types, line)
		}
	}
	sort.Strings(types)
	return types, nil
}

// CreateOptions shape one create call beyond the kit set itself.
type CreateOptions struct {
	// Args are kit argument overrides, passed as --arg name=value.
	Args map[string]string

	// SkillsHostMode, when set, is the host-side skills setting the
	// adapter must arrange for this sandbox, passed as
	// --skills-host-mode. Empty means the contract's default: the most
	// permissive setting. The host's half of the access bound is
	// unobservable without this, because a suite that always arranges a
	// permissive host can only ever see the kit's half narrow.
	SkillsHostMode string
}

// Create composes a kit set into a running sandbox and returns its id.
func (a *Adapter) Create(ctx context.Context, kits []string, opts CreateOptions) (string, error) {
	argv := append([]string{"create"}, kits...)
	if opts.SkillsHostMode != "" {
		argv = append(argv, "--skills-host-mode", opts.SkillsHostMode)
	}
	for _, name := range sortedKeys(opts.Args) {
		argv = append(argv, "--arg", name+"="+opts.Args[name])
	}
	res, err := a.run(ctx, argv...)
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case 0:
	case ExitRefused:
		return "", &RefusedError{Detail: strings.TrimSpace(res.Stderr)}
	default:
		return "", fmt.Errorf("create: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	id := strings.TrimSpace(res.Stdout)
	if id == "" {
		return "", errors.New("create succeeded but printed no sandbox id")
	}
	return id, nil
}

// Exec runs a command inside a sandbox, proxying its exit status.
func (a *Adapter) Exec(ctx context.Context, id string, argv ...string) (Result, error) {
	return a.run(ctx, append([]string{"exec", id, "--"}, argv...)...)
}

// Recreate replaces the sandbox's container — a fresh writable layer —
// preserving only declared volume state. Stop/start preserves the entire
// filesystem, so it cannot tell a volume from an ordinary directory;
// recreate is the observation that can.
func (a *Adapter) Recreate(ctx context.Context, id string) error {
	return a.mustRun(ctx, "recreate", id)
}

// Stop and Start bracket a reboot, which is what separates install hooks
// from startup hooks.
func (a *Adapter) Stop(ctx context.Context, id string) error  { return a.mustRun(ctx, "stop", id) }
func (a *Adapter) Start(ctx context.Context, id string) error { return a.mustRun(ctx, "start", id) }

// Remove discards a sandbox.
func (a *Adapter) Remove(ctx context.Context, id string) error { return a.mustRun(ctx, "rm", id) }

// ExitRefused is the status an adapter returns when the runtime declined a
// request it understood. Any other non-zero status is a failure, and the
// difference matters: requirements satisfied by refusing something would
// otherwise be satisfied equally by an adapter that cannot run at all.
const ExitRefused = 2

// RefusedError reports a request the runtime declined deliberately.
type RefusedError struct {
	Detail string
}

func (e *RefusedError) Error() string {
	return "runtime refused the request: " + e.Detail
}

func (a *Adapter) mustRun(ctx context.Context, argv ...string) error {
	res, err := a.run(ctx, argv...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: exit %d: %s", argv[0], res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (a *Adapter) run(ctx context.Context, argv ...string) (Result, error) {
	// A relative adapter path is the caller's, resolved against the
	// caller's working directory — Dir must not retarget it.
	path := a.Path
	if a.Dir != "" && !filepath.IsAbs(path) && strings.ContainsRune(path, os.PathSeparator) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Dir = a.Dir
	// Killing the adapter does not close pipes a descendant runtime
	// process still holds; without a wait bound, a hung descendant
	// defeats every timeout above this call.
	cmd.WaitDelay = 30 * time.Second
	cmd.Env = append(cmd.Environ(), a.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		// Cancellation surfaces as an ExitError too, and classifying it
		// by exit code would turn the caller's deadline into a runtime
		// non-conformance finding.
		if ctx.Err() != nil {
			return res, fmt.Errorf("run adapter %s %s: %w", a.Path, argv[0], ctx.Err())
		}
		res.ExitCode = exitErr.ExitCode()
	default:
		// The adapter itself could not be run: not a verdict about the
		// runtime, so it surfaces as an error rather than a failure.
		return res, fmt.Errorf("run adapter %s %s: %w", a.Path, argv[0], err)
	}
	return res, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
