package assemble

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestWalkLayerVerifiedHashesCompleteStream(t *testing.T) {
	plain := tarLayer(t, false, map[string]string{"tool": "content"})
	// Tar stops at its end markers, but the diff ID covers all bytes.
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{0}, 512)...)
	for _, compression := range []string{"tar", "gzip", "zstd"} {
		t.Run(compression, func(t *testing.T) {
			blob := padded
			var compressed bytes.Buffer
			switch compression {
			case "gzip":
				w := gzip.NewWriter(&compressed)
				_, err := w.Write(padded)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				blob = compressed.Bytes()
			case "zstd":
				w, err := zstd.NewWriter(&compressed)
				require.NoError(t, err)
				_, err = w.Write(padded)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				blob = compressed.Bytes()
			}
			var files []string
			require.NoError(t, WalkLayerVerified(bytes.NewReader(blob), digest.FromBytes(padded), func(h *tar.Header) error {
				if h.Typeflag != tar.TypeDir {
					files = append(files, h.Name)
				}
				return nil
			}))
			require.Equal(t, []string{"tool"}, files)
			err := WalkLayerVerified(bytes.NewReader(blob), digest.FromBytes(plain), func(*tar.Header) error { return nil })
			require.ErrorContains(t, err, "diff ID mismatch")
		})
	}
}

func TestWalkLayerVerifiedReadsGzipTrailer(t *testing.T) {
	blob := tarLayer(t, true, map[string]string{"tool": strings.Repeat("x", 5000)})
	blob[len(blob)-8] ^= 0xff // Corrupt the CRC after a valid tar archive.
	err := WalkLayerVerified(bytes.NewReader(blob), digest.FromString("unused"), func(*tar.Header) error { return nil })
	require.ErrorIs(t, err, gzip.ErrChecksum)
}
