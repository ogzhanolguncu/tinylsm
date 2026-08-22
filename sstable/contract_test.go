//go:build !nocontract

// Panic-asserting tests. Excluded under -tags nocontract, where the contracts
// they assert do not exist.
package sstable

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// The ordering contract short-circuits on an empty block, so keys.Compare's
// length check never sees a block's first entry — hence a separate one.
func TestBlockBuilderRejectsShortKey(t *testing.T) {
	for n := range keys.TrailerSize {
		short := make([]byte, n)

		t.Run(fmt.Sprintf("as first entry/len %d", n), func(t *testing.T) {
			b := newBlockBuilder()
			require.Panics(t, func() { b.Add(short, []byte("v")) })
		})

		t.Run(fmt.Sprintf("as later entry/len %d", n), func(t *testing.T) {
			b := newBlockBuilder()
			b.Add(ik(t, "a", 1, keys.KindPut), []byte("v"))
			require.Panics(t, func() { b.Add(short, []byte("v")) })
		})
	}
}

func TestBlockBuilderRejectsOutOfOrderKeys(t *testing.T) {
	cases := []struct {
		name   string
		first  []byte
		second []byte
	}{
		{"descending user key", ik(t, "b", 1, keys.KindPut), ik(t, "a", 1, keys.KindPut)},
		{"identical internal key", ik(t, "a", 1, keys.KindPut), ik(t, "a", 1, keys.KindPut)},
		// Internal order is (userKey asc, seq DESC), so seq 2 belongs before seq 1.
		{"same user key, ascending seq", ik(t, "a", 1, keys.KindPut), ik(t, "a", 2, keys.KindPut)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBlockBuilder()
			b.Add(tc.first, []byte("v1"))
			require.Panics(t, func() { b.Add(tc.second, []byte("v2")) })
		})
	}
}

func TestBlockIterRejectsUseWhenExhausted(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "a", 1, keys.KindPut), []byte("v"))
	blk, err := newBlock(b.Finish())
	require.NoError(t, err)

	it := &blockIter{b: blk}
	it.Next() // consume the only entry

	require.Panics(t, func() { it.Key() })
	require.Panics(t, func() { it.Value() })
	require.Panics(t, func() { it.Next() })
}

func TestTableIterRejectsUseWhenExhausted(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(11))
	writeTable(t, path, 40)
	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	t.Run("fresh, never positioned", func(t *testing.T) {
		it := tbl.NewIterator()
		require.False(t, it.Valid())
		require.Panics(t, func() { it.Key() })
		require.Panics(t, func() { it.Value() })
		require.Panics(t, func() { it.Next() })
	})

	t.Run("walked off the end", func(t *testing.T) {
		it := tbl.NewIterator()
		for it.SeekToFirst(); it.Valid(); it.Next() {
		}
		require.Panics(t, func() { it.Key() })
		require.Panics(t, func() { it.Value() })
		require.Panics(t, func() { it.Next() })
	})

	t.Run("seeked past every key", func(t *testing.T) {
		it := tbl.NewIterator()
		it.Seek(ik(t, "zzz", 99, keys.KindPut))
		require.False(t, it.Valid())
		require.Panics(t, func() { it.Key() })
	})
}
