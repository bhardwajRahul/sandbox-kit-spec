package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Observe the real shell adapter at its CLI boundary without creating sandboxes.
const skillsStub = `#!/bin/sh
set -eu
if [ "${1:-}" = --app-name ]; then shift 2; fi
case "$1" in
skills) printf '{"store":"%s"}\n' "$STUB_STORE" ;;
create)
  shift
  mode=unset
  while [ $# -gt 0 ]; do
    if [ "$1" = --skills ]; then mode="$2"; shift; fi
    shift
  done
  contents=missing
  if [ -d "$STUB_STORE" ]; then
    contents=empty
    if [ -n "$(ls -A "$STUB_STORE")" ]; then contents=populated; fi
  fi
  printf '%s %s\n' "$mode" "$contents" >"$STUB_OBSERVED"
  if [ "${STUB_FAIL:-}" = yes ]; then echo 'create failed unexpectedly' >&2; exit 3; fi
  ;;
rm) ;;
ls) echo '[]' ;;
*) echo "unexpected stub call: $*" >&2; exit 1 ;;
esac
`

func skillsAdapter(t *testing.T) (*Adapter, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "sbx")
	require.NoError(t, os.WriteFile(stub, []byte(skillsStub), 0700))
	store, state, observed := filepath.Join(dir, "store"), filepath.Join(dir, "state"), filepath.Join(dir, "observed")
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{
		"SBX=" + stub, "SBX_TCK_STATE=" + state, "SBX_TCK_APP_NAME=skills-probe",
		"SBX_TCK_CAPABILITIES=com.docker.sandbox/agent-skills@1", "SBX_TCK_SHARE_SKILLS_STORE=1",
		"KIT_TCK_SKILL_NAME=probe", "STUB_STORE=" + store, "STUB_OBSERVED=" + observed, "STUB_FAIL=",
	}
	return a, store, state, observed
}

func TestSbxSkillsStoreScenariosRestoreContent(t *testing.T) {
	for _, scenario := range []string{"missing", "empty"} {
		for _, existing := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				t.Run(scenario+map[bool]string{false: "/new", true: "/existing"}[existing]+map[bool]string{false: "/success", true: "/failure"}[failed], func(t *testing.T) {
					a, store, state, observed := skillsAdapter(t)
					if existing {
						require.NoError(t, os.MkdirAll(store, 0700))
						require.NoError(t, os.WriteFile(filepath.Join(store, ".existing"), []byte("preserve me"), 0600))
					}
					if failed {
						a.Env = append(a.Env, "STUB_FAIL=yes")
					}
					id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SkillsHostStore: scenario})
					if failed {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						require.NoError(t, a.Remove(t.Context(), id))
					}
					raw, err := os.ReadFile(observed)
					require.NoError(t, err)
					require.Equal(t, "readwrite "+scenario+"\n", string(raw), "sharing stays enabled and marker seeding is suppressed")
					if existing {
						raw, err = os.ReadFile(filepath.Join(store, ".existing"))
						require.NoError(t, err)
						require.Equal(t, "preserve me", string(raw))
					} else {
						require.NoDirExists(t, store)
					}
					require.NoFileExists(t, filepath.Join(state, "skills-store.path"))
					require.NoDirExists(t, filepath.Join(state, "skills-store.backup"))
					// The next ordinary create must receive the normal seeded store.
					a.Env = append(a.Env, "STUB_FAIL=")
					id, err = a.Create(t.Context(), []string{"workload"}, CreateOptions{})
					require.NoError(t, err)
					raw, err = os.ReadFile(observed)
					require.NoError(t, err)
					require.Equal(t, "readwrite populated\n", string(raw))
					require.FileExists(t, filepath.Join(store, "probe", "SKILL.md"))
					require.NoError(t, a.Remove(t.Context(), id))
				})
			}
		}
	}
}

func TestSbxSkillsStoreProbePreservesUnexpectedContent(t *testing.T) {
	a, store, state, _ := skillsAdapter(t)
	require.NoError(t, os.MkdirAll(store, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(store, "original"), []byte("original"), 0600))
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SkillsHostStore: "empty"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store, "new"), []byte("new"), 0600))
	err = a.Remove(t.Context(), id)
	require.ErrorContains(t, err, "probe store is not empty")
	require.FileExists(t, filepath.Join(store, "new"))
	require.FileExists(t, filepath.Join(state, "skills-store.backup", "original"))
	require.FileExists(t, filepath.Join(state, "skills-store.path"))
}

// Observe SSH_AUTH_SOCK at the real adapter's create boundary: the suite
// socket must be exported, an inherited host socket must not substitute
// when --ssh-agent is absent, and recreate must replay the original.
const sshStub = `#!/bin/sh
set -eu
if [ "${1:-}" = --app-name ]; then shift 2; fi
case "$1" in
create)
  if [ -n "${SSH_AUTH_SOCK+x}" ]; then
    printf 'set:%s\n' "$SSH_AUTH_SOCK" >>"$STUB_OBSERVED"
  else
    printf 'unset\n' >>"$STUB_OBSERVED"
  fi
  ;;
rm) ;;
ls) echo '[]' ;;
*) echo "unexpected stub call: $*" >&2; exit 1 ;;
esac
`

func sshAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "sbx")
	require.NoError(t, os.WriteFile(stub, []byte(sshStub), 0700))
	state, observed := filepath.Join(dir, "state"), filepath.Join(dir, "observed")
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{
		"SBX=" + stub, "SBX_TCK_STATE=" + state, "SBX_TCK_APP_NAME=ssh-probe",
		"SBX_TCK_CAPABILITIES=com.docker.sandbox/ssh-agent@1",
		"STUB_OBSERVED=" + observed,
		// A host agent the adapter must not inherit when the suite withholds
		// --ssh-agent (§2.3).
		"SSH_AUTH_SOCK=" + filepath.Join(dir, "host-agent.sock"),
	}
	return a, observed
}

func TestSbxSSHAgentExportsOfferedSocket(t *testing.T) {
	a, observed := sshAdapter(t)
	socket := filepath.Join(t.TempDir(), "suite-agent.sock")
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SSHAgent: socket})
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "set:"+socket+"\n", string(raw))
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxSSHAgentClearsInheritedSocketWhenAbsent(t *testing.T) {
	a, observed := sshAdapter(t)
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{})
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "unset\n", string(raw), "host SSH_AUTH_SOCK must not reach sbx without --ssh-agent")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxSSHAgentReplaysBindingOnRecreate(t *testing.T) {
	a, observed := sshAdapter(t)
	socket := filepath.Join(t.TempDir(), "suite-agent.sock")
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SSHAgent: socket})
	require.NoError(t, err)
	// Point the process env at a different socket; recreate must still use
	// the remembered suite agent, not this one.
	a.Env = append(a.Env, "SSH_AUTH_SOCK="+filepath.Join(t.TempDir(), "other-agent.sock"))
	require.NoError(t, a.Recreate(t.Context(), id))
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "set:"+socket+"\nset:"+socket+"\n", string(raw))
	require.NoError(t, a.Remove(t.Context(), id))
}
