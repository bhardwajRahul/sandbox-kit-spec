package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain builds the fake runtime's SSH agent relay once for every test
// that drives the fake: the relay has to verify session-binding
// signatures, which is Go's job rather than the shell's, and building it
// per exec would multiply the suite's run time by the build.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "kit-tck-relay-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	relay := filepath.Join(dir, "ssh-relay")
	build := exec.Command("go", "build", "-o", relay, "./testdata/ssh-relay")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build the fake's ssh-relay:", err)
		return 1
	}
	// Inherited by every fake-adapter run, whichever test builds the
	// adapter's environment.
	if err := os.Setenv("KIT_TCK_FAKE_RELAY", relay); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
