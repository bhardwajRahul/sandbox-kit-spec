package sandbox

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// embeddedFixtures ships the fixture kits inside any binary importing the
// suite, so an installed kit-tck works outside a source checkout.
//
//go:embed testdata/fixtures
var embeddedFixtures embed.FS

// MaterializeFixtures writes the embedded fixture kits to a temporary
// directory and returns its fixtures root with a cleanup that removes it.
// Adapters take fixture references as paths, so the embedded tree has to
// become real files, and files somebody has to delete again.
func MaterializeFixtures() (string, func(), error) {
	root, err := os.MkdirTemp("", "kit-tck-fixtures-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	err = fs.WalkDir(embeddedFixtures, "testdata/fixtures", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("testdata/fixtures", filepath.FromSlash(name))
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := embeddedFixtures.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}
