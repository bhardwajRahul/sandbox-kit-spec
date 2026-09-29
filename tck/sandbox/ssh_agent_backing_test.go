package sandbox

import (
	"bytes"
	"encoding/binary"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackingAgentCountsFragmentedRequestTypes(t *testing.T) {
	var wire []byte
	for _, body := range [][]byte{{11}, {24, 11, 13}, {13}, {24}} {
		wire = binary.BigEndian.AppendUint32(wire, uint32(len(body)))
		wire = append(wire, body...)
	}
	for _, size := range []int{1, 2, 4, 5, len(wire)} {
		var count atomic.Int64
		var types [256]atomic.Int64
		counter := frameCounter{r: bytes.NewReader(wire), n: &count, types: &types}
		buf := make([]byte, size)
		for {
			_, err := counter.Read(buf)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}
		require.EqualValues(t, 4, count.Load())
		require.EqualValues(t, 1, types[11].Load())
		require.EqualValues(t, 1, types[13].Load())
		require.EqualValues(t, 2, types[24].Load(), "only the first body byte names the operation")
	}
}
