package sstable

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// scan drains an iterator from its current position and returns the keys it
// yielded. It never calls SeekToFirst, so callers choose the starting point.
func scan(t *testing.T, it *tableIter) [][]byte {
	t.Helper()
	var got [][]byte
	for ; it.Valid(); it.Next() {
		got = append(got, bytes.Clone(it.Key()))
	}
	return got
}

func TestTableIterScansEveryEntryInOrder(t *testing.T) {
	const n = 40
	path := filepath.Join(t.TempDir(), FileName(1))
	writeTable(t, path, n)

	idx, _, _ := parseTable(t, path)
	require.GreaterOrEqual(t, len(idx), 3, "input must span several blocks")

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	it := tbl.NewIterator()
	it.SeekToFirst()
	got := scan(t, it)
	require.NoError(t, it.Error())

	want := make([][]byte, n)
	for i := range want {
		want[i] = ik(t, fmt.Sprintf("key%04d", i), uint64(i+1), keys.KindPut)
	}
	require.Equal(t, want, got, "every entry, in order, across block boundaries")
}

func TestTableIterSeek(t *testing.T) {
	const n = 40
	path := filepath.Join(t.TempDir(), FileName(2))
	writeTable(t, path, n)

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	tests := []struct {
		name      string
		target    string
		wantValid bool
		wantKey   string // first key at or after target
		wantRest  int    // entries from there to the end
	}{
		{"exact key in the first block", "key0000", true, "key0000", 40},
		{"exact key in a later block", "key0025", true, "key0025", 15},
		{"between two keys", "key0025x", true, "key0026", 14},
		{"before every key", "aaa", true, "key0000", 40},
		{"past every key", "zzz", false, "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := tbl.NewIterator()
			it.Seek(ik(t, tc.target, uint64(n+1), keys.KindPut))
			require.Equal(t, tc.wantValid, it.Valid())
			if !tc.wantValid {
				require.NoError(t, it.Error(), "past-the-end is not a failure")
				return
			}

			uk, _, _, err := keys.Decode(it.Key())
			require.NoError(t, err)
			require.Equal(t, tc.wantKey, string(uk))

			// Seek must leave the iterator walkable, boundaries included.
			require.Len(t, scan(t, it), tc.wantRest)
			require.NoError(t, it.Error())
		})
	}
}

// SeekToFirst must reposition indexIt, not resume from wherever Seek left it.
func TestTableIterSeekToFirstAfterSeek(t *testing.T) {
	const n = 40
	path := filepath.Join(t.TempDir(), FileName(3))
	writeTable(t, path, n)

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	it := tbl.NewIterator()
	it.Seek(ik(t, "key0030", uint64(n+1), keys.KindPut))
	require.True(t, it.Valid())

	it.SeekToFirst()
	require.Len(t, scan(t, it), n)
	require.NoError(t, it.Error())
}

// A scan that dies mid-table must not look like a scan that finished. Without
// Error(), a caller reads N entries, sees Valid() == false, and moves on.
func TestTableIterReportsCorruptionInsteadOfEndOfScan(t *testing.T) {
	golden := filepath.Join(t.TempDir(), FileName(4))
	writeTable(t, golden, 40)

	idx, raw, _ := parseTable(t, golden)
	require.GreaterOrEqual(t, len(idx), 3)
	raw[idx[1].off] ^= 0xff // damage the SECOND data block: block 0 still scans

	bad := filepath.Join(t.TempDir(), FileName(5))
	require.NoError(t, os.WriteFile(bad, raw, 0o644))

	tbl, err := openTable(bad)
	require.NoError(t, err, "index and footer are intact")
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	it := tbl.NewIterator()
	it.SeekToFirst()
	got := scan(t, it)

	require.ErrorIs(t, it.Error(), ErrBlockChecksum)
	require.NotEmpty(t, got, "block 0 is undamaged")
	require.Less(t, len(got), 40, "the scan must stop at the damaged block")

	// Seeking straight into the damaged block must fail the same way, and must
	// not touch dataIt afterwards: on a fresh iterator it is still nil.
	seeked := tbl.NewIterator()
	lastKey := idx[1].lastKey
	require.NotPanics(t, func() { seeked.Seek(lastKey) })
	require.False(t, seeked.Valid())
	require.ErrorIs(t, seeked.Error(), ErrBlockChecksum)
}

// The first error wins: a later success must not resurrect a dead iterator.
func TestTableIterErrorIsSticky(t *testing.T) {
	golden := filepath.Join(t.TempDir(), FileName(6))
	writeTable(t, golden, 40)

	idx, raw, _ := parseTable(t, golden)
	require.GreaterOrEqual(t, len(idx), 3)
	raw[idx[1].off] ^= 0xff

	bad := filepath.Join(t.TempDir(), FileName(7))
	require.NoError(t, os.WriteFile(bad, raw, 0o644))

	tbl, err := openTable(bad)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	it := tbl.NewIterator()
	it.SeekToFirst()
	scan(t, it)
	require.ErrorIs(t, it.Error(), ErrBlockChecksum)

	// block 0 is undamaged, so this load succeeds — the iterator must stay dead.
	it.SeekToFirst()
	require.ErrorIs(t, it.Error(), ErrBlockChecksum)
	require.False(t, it.Valid())
}
