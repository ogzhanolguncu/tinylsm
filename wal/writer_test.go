package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Clean round-trip: records written through the real Writer must come back
// out of Replay byte-identical in every field. The other replay tests all
// truncate or corrupt the bytes first; none proves an untouched log survives
// a Writer -> disk -> Replay cycle with value and kind intact (TornTailEveryCut
// checks only key+seq). Covers put, tombstone (nil value), and empty value.
func TestWriterReplayRoundTrip(t *testing.T) {
	entries := []Entry{
		{key: []byte("cat"), value: []byte("purr"), seq: 1, kind: KindPut},
		{key: []byte("dog"), value: nil, seq: 2, kind: KindDelete},
		{key: []byte("bird"), value: []byte{}, seq: 3, kind: KindPut},
		{key: []byte{0x00, 0xFF, 0x80}, value: []byte{0x01, 0x00, 0x02}, seq: 4, kind: KindPut},
	}

	path := filepath.Join(t.TempDir(), "000000001.wal")
	w, err := NewWriter(path)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, w.Append(e))
	}
	require.NoError(t, w.Close())

	// capture the on-disk bytes BEFORE replay so we can prove replay leaves a
	// clean log untouched (string/[]byte compares below dodge testify's
	// nil-vs-empty-slice trap).
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	got, err := Replay(path)
	require.NoError(t, err)
	require.Len(t, got, len(entries))
	for i, want := range entries {
		require.Equalf(t, want.seq, got[i].seq, "entry %d seq", i)
		require.Equalf(t, want.kind, got[i].kind, "entry %d kind", i)
		require.Equalf(t, string(want.key), string(got[i].key), "entry %d key", i)
		require.Equalf(t, string(want.value), string(got[i].value), "entry %d value", i)
	}

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "clean replay must not truncate")
}

// A writer that fails once must refuse all subsequent appends: a partial
// record at the tail is recoverable (torn tail), but a successful append
// AFTER it would bury garbage mid-file and make replay reject the log.
func TestAppendAfterFailureRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "000000001.wal")
	w, err := NewWriter(path)
	require.NoError(t, err)

	good := Entry{key: []byte("k1"), value: []byte("v1"), seq: 1, kind: KindPut}
	require.NoError(t, w.Append(good))

	// force the next write to fail: yank the fd out from under the writer
	require.NoError(t, w.f.Close())

	err = w.Append(Entry{key: []byte("k2"), value: []byte("v2"), seq: 2, kind: KindPut})
	require.Error(t, err, "append on closed file must fail")

	// writer must now be poisoned and refuse before touching the file
	err = w.Append(Entry{key: []byte("k3"), value: []byte("v3"), seq: 3, kind: KindPut})
	require.ErrorContains(t, err, "writer is broken")

	// the acknowledged record must still be intact on disk
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	e, _, err := decode(data)
	require.NoError(t, err)
	require.Equal(t, good.key, e.key)
	require.Equal(t, good.value, e.value)
	require.Equal(t, good.seq, e.seq)
}
