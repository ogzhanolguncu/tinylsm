package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriterAppendSingleEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MANIFEST-000001")

	w, err := Create(path)
	require.NoError(t, err)

	err = w.Append(VersionEdit{
		NextFileNum: 1,
		LastSeq:     10,
	})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// File should exist and be non-empty.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
}

func TestWriterAppendThenFold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MANIFEST-000001")

	w, err := Create(path)
	require.NoError(t, err)

	// First edit: set counters.
	require.NoError(t, w.Append(VersionEdit{
		NextFileNum: 1,
		LastSeq:     0,
	}))

	// Second edit: add a file to L0.
	require.NoError(t, w.Append(VersionEdit{
		AddFiles:    []FileMeta{{Level: 0, FileNum: 1}},
		NextFileNum: 2,
		LastSeq:     100,
	}))

	// Third edit: add another file, delete the first.
	require.NoError(t, w.Append(VersionEdit{
		AddFiles:    []FileMeta{{Level: 0, FileNum: 2}},
		DelFiles:    []FileMeta{{Level: 0, FileNum: 1}},
		NextFileNum: 3,
		LastSeq:     200,
	}))
	require.NoError(t, w.Close())

	// Fold should reconstruct the final state.
	v, err := Fold(path)
	require.NoError(t, err)

	require.Equal(t, uint64(3), v.NextFileNum)
	require.Equal(t, uint64(200), v.LastSeq)
	require.Len(t, v.Files[0], 1, "L0 should have one file after add+delete")
	require.Equal(t, uint64(2), v.Files[0][0].FileNum)
}
