package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/pkg/frame"
	"github.com/stretchr/testify/require"
)

// writeThreeEdits writes three edits, each adding one L0 file, and returns
// the file bytes plus the offset where each record ends.
func writeThreeEdits(t *testing.T) ([]byte, []int) {
	path := filepath.Join(t.TempDir(), "MANIFEST-000001")
	w, err := Create(path)
	require.NoError(t, err)

	var ends []int
	for i := uint64(1); i <= 3; i++ {
		require.NoError(t, w.Append(VersionEdit{AddFiles: []FileMeta{{Level: 0, FileNum: i}}}))
		info, err := os.Stat(path)
		require.NoError(t, err)
		ends = append(ends, int(info.Size()))
	}
	require.NoError(t, w.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data, ends
}

func foldBytes(t *testing.T, data []byte) (*Version, error) {
	path := filepath.Join(t.TempDir(), "MANIFEST-000001")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return Fold(path)
}

func TestFoldTornTailEveryCut(t *testing.T) {
	data, ends := writeThreeEdits(t)

	for cut := 0; cut <= len(data); cut++ {
		complete := 0
		for _, e := range ends {
			if e <= cut {
				complete++
			}
		}
		v, err := foldBytes(t, data[:cut])
		require.NoErrorf(t, err, "cut %d", cut)
		require.Lenf(t, v.Files[0], complete, "cut %d", cut)
	}
}

func TestFoldBadCRCInLastRecordIsTornTail(t *testing.T) {
	data, ends := writeThreeEdits(t)
	data[ends[1]+frame.HeaderSize] ^= 0xFF

	v, err := foldBytes(t, data)
	require.NoError(t, err)
	require.Len(t, v.Files[0], 2)
}

func TestFoldRefusesMidFileCorruption(t *testing.T) {
	data, _ := writeThreeEdits(t)
	data[frame.HeaderSize] ^= 0xFF

	_, err := foldBytes(t, data)
	require.ErrorIs(t, err, frame.ErrChecksum)
}
