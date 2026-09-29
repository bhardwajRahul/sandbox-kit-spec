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
