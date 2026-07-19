package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
