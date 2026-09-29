package overlayfs

import (
	"archive/tar"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalkVisitsComponentsInPreorder(t *testing.T) {
	fs := New()
	layer := fs.NewLayer()
	for _, name := range []string{"z/file", "a/.wh.gone", "a/keep", "a/.wh..wh..opq"} {
		require.True(t, layer.Add(&tar.Header{Name: name, Typeflag: tar.TypeReg}))
	}
	fs.Apply(layer)
	var entries []Entry
	require.NoError(t, fs.Walk(func(entry Entry) error { entries = append(entries, entry); return nil }))
	require.Equal(t, []Entry{
		{Name: "", Depth: 0, Directory: true},
		{Name: "a", Depth: 1, Directory: true, Opaque: true},
		{Name: "gone", Depth: 2, Whiteout: true},
		{Name: "keep", Depth: 2},
		{Name: "z", Depth: 1, Directory: true},
		{Name: "file", Depth: 2},
	}, entries)
	stop := errors.New("stop walking")
	visits := 0
	require.ErrorIs(t, fs.Walk(func(Entry) error { visits++; return stop }), stop)
	require.Equal(t, 1, visits)
}
