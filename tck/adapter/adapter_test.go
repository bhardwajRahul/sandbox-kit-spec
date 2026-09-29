package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateTransportsContainerEnvironmentLiterally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$ARGV_FILE\"\nprintf 'sandbox-id\\n'\n"), 0700))
	argvFile := filepath.Join(dir, "argv")
	a := New(path)
	a.Env = []string{"ARGV_FILE=" + argvFile}
	value := "spaces = equals\nquotes'\" $HOME $(touch forbidden)"
	id, err := a.Create(t.Context(), []string{"workload", "mixin"}, CreateOptions{
		Args: map[string]string{"greeting": "argument"},
		Env:  map[string]string{"Z_VALUE": value, "A_EMPTY": ""},
	})
	require.NoError(t, err)
	require.Equal(t, "sandbox-id", id)
	raw, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	require.Equal(t, []string{"create", "workload", "mixin", "--arg", "greeting=argument", "--env", "A_EMPTY=", "--env", "Z_VALUE=" + value}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
}
