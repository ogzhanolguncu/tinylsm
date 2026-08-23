package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildWAL writes the given entries through the real Writer and returns the
// file's bytes plus each record's end offset within the file.
func buildWAL(t *testing.T, entries []Entry) ([]byte, []int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.wal")
	w, err := NewWriter(path)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, w.Append(e))
	}
	require.NoError(t, w.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var ends []int
	off := 0
	for off < len(data) {
		_, n, derr := decode(data[off:])
		require.NoError(t, derr)
		off += n
		ends = append(ends, off)
	}
	return data, ends
}

var replayFixture = []Entry{
	{Key: []byte("cat"), Value: []byte("purr"), Seq: 1, Kind: KindPut},
	{Key: []byte("dog"), Value: []byte("woof"), Seq: 2, Kind: KindPut},
	{Key: []byte("cat"), Value: nil, Seq: 3, Kind: KindDelete},
}

// Chop the file at EVERY byte boundary. No cut is
// corruption — a shortened file is exactly what a crash mid-append leaves.
// Replay must recover every record that fully fits, error on none, and
// truncate the file back to the last complete record.
func TestReplayTornTailEveryCut(t *testing.T) {
	data, ends := buildWAL(t, replayFixture)
	dir := t.TempDir()

	for cut := 0; cut <= len(data); cut++ {
		path := filepath.Join(dir, fmt.Sprintf("cut-%d.wal", cut))
		require.NoError(t, os.WriteFile(path, data[:cut], 0o644))

		got, err := Replay(path)
		require.NoError(t, err, "cut=%d is a torn tail, must never be corruption", cut)

		wantN, wantSize := 0, 0
		for _, end := range ends {
			if end <= cut {
				wantN++
				wantSize = end
			}
		}
		require.Len(t, got, wantN, "cut=%d", cut)
		for i := range got {
			require.Equal(t, string(replayFixture[i].Key), string(got[i].Key), "cut=%d entry=%d", cut, i)
			require.Equal(t, replayFixture[i].Seq, got[i].Seq, "cut=%d entry=%d", cut, i)
		}

		// replay must have truncated the torn bytes off the file
		st, err := os.Stat(path)
		require.NoError(t, err)
		require.EqualValues(t, wantSize, st.Size(), "cut=%d: file must end at last complete record", cut)
	}
}

// A damaged record with valid records AFTER it cannot be a crash artifact —
// the writer never writes past a failure. Replay must refuse, return no
// partial data, and leave the file untouched.
func TestReplayRefusesMidFileCorruption(t *testing.T) {
	data, ends := buildWAL(t, replayFixture)

	// flip one payload byte inside the FIRST record
	corrupted := append([]byte{}, data...)
	corrupted[headerSize] ^= 0xFF
	require.Greater(t, len(ends), 1, "fixture must have records after the first")

	path := filepath.Join(t.TempDir(), "corrupt.wal")
	require.NoError(t, os.WriteFile(path, corrupted, 0o644))

	got, err := Replay(path)
	require.Error(t, err, "bad record followed by valid records = corruption")
	require.Nil(t, got, "no partial results on corruption")

	st, serr := os.Stat(path)
	require.NoError(t, serr)
	require.EqualValues(t, len(corrupted), st.Size(), "corrupt file must not be truncated")
}

// Same damage in the LAST record is indistinguishable from a half-written
// sector at crash time: torn tail, not corruption.
func TestReplayBadCRCInLastRecordIsTornTail(t *testing.T) {
	data, ends := buildWAL(t, replayFixture)

	corrupted := append([]byte{}, data...)
	corrupted[len(corrupted)-1] ^= 0xFF // inside last record's payload

	path := filepath.Join(t.TempDir(), "tail.wal")
	require.NoError(t, os.WriteFile(path, corrupted, 0o644))

	got, err := Replay(path)
	require.NoError(t, err, "bad frame at EOF is a torn tail")
	require.Len(t, got, len(ends)-1, "all records before the tear survive")

	st, serr := os.Stat(path)
	require.NoError(t, serr)
	require.EqualValues(t, ends[len(ends)-2], st.Size(), "tail truncated at last good record")
}
