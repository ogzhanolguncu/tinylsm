package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// parseTable reads a finished .sst the way a reader will: footer from the tail,
// then the index block. It returns the decoded index, the whole file, and where
// the index starts.
func parseTable(t *testing.T, path string) (idx []indexEntry, raw []byte, indexOff uint64) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Greater(t, uint64(len(raw)), footerSize)

	footer := raw[uint64(len(raw))-footerSize:]
	require.Equal(t, []byte("OzLSM\x00\x00\x01"), footer[16:24], "magic")
	indexOff = binary.LittleEndian.Uint64(footer[0:8])
	indexSize := binary.LittleEndian.Uint64(footer[8:16])
	require.Equal(t, uint64(len(raw)), indexOff+indexSize+footerSize, "footer must cover the file")

	blk, err := newBlock(raw[indexOff : indexOff+indexSize])
	require.NoError(t, err)

	for it := (&blockIter{b: blk}); it.Valid(); it.Next() {
		v := it.Value()
		require.Len(t, v, 16, "index value is a fixed offset+size pair")
		idx = append(idx, indexEntry{
			lastKey: bytes.Clone(it.Key()),
			off:     binary.LittleEndian.Uint64(v[0:8]),
			size:    binary.LittleEndian.Uint64(v[8:16]),
		})
	}
	return idx, raw, indexOff
}

func TestTableWriterRoundTrip(t *testing.T) {
	type entry struct{ key, val []byte }

	for _, n := range []int{1, 40} {
		t.Run(fmt.Sprintf("%dentries", n), func(t *testing.T) {
			entries := make([]entry, n)
			for i := range entries {
				kind, val := keys.KindPut, bytes.Repeat([]byte{byte(i)}, 512)
				if i == 1 {
					kind, val = keys.KindDelete, []byte{}
				}
				entries[i] = entry{ik(t, fmt.Sprintf("key%04d", i), uint64(i+1), kind), val}
			}

			path := filepath.Join(t.TempDir(), FileName(7))
			tw, err := newTableWriter(path)
			require.NoError(t, err)
			for _, e := range entries {
				require.NoError(t, tw.Add(e.key, e.val))
			}
			require.NoError(t, tw.Finish())

			idx, raw, indexOff := parseTable(t, path)
			require.NotEmpty(t, idx)
			if n > 1 {
				require.GreaterOrEqual(t, len(idx), 3, "input must span several blocks")
			}

			var got []entry
			var nextOff uint64
			for i, e := range idx {
				require.Equal(t, nextOff, e.off, "data block %d must start where %d ended", i, i-1)
				nextOff += e.size

				blk, err := newBlock(raw[e.off : e.off+e.size])
				require.NoError(t, err)

				var lastKey []byte
				for it := (&blockIter{b: blk}); it.Valid(); it.Next() {
					lastKey = bytes.Clone(it.Key())
					got = append(got, entry{lastKey, bytes.Clone(it.Value())})
				}
				require.Equal(t, e.lastKey, lastKey, "index key %d must be its block's last key", i)
			}
			require.Equal(t, indexOff, nextOff, "index must start after the last data block")
			require.Equal(t, entries, got)
		})
	}
}

// A reused file number must never silently destroy a live SSTable.
func TestNewTableWriterRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(1))
	tw, err := newTableWriter(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tw.f.Close() })

	_, err = newTableWriter(path)
	require.ErrorIs(t, err, os.ErrExist)
}
