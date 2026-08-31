package sstable

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// The footer's maxSeq must be the largest seq anywhere in the file. Entries are
// sorted by user key first, so the largest seq can sit on the very first entry
// of the very first block — nowhere near the last key of the file, and nowhere
// near anything the index records. Reading it off the last key (of a block, of
// the index, or of the table) reports a stale seq, and a stale seq makes the
// next reopen hand out numbers a live SSTable is already using.
func TestFooterMaxSeqIsGlobalMaximumNotLastKey(t *testing.T) {
	// "a" carries the highest seq and sorts first; "z" carries the lowest and
	// sorts last. Any position-based guess gets 5 instead of 9999.
	entries := []struct {
		userKey string
		seq     uint64
	}{
		{"a", 9999},
		{"b", 12},
		{"c", 7},
		{"m", 3},
		{"y", 8},
		{"z", 5},
	}

	path := filepath.Join(t.TempDir(), FileName(1))
	tw, err := NewWriter(path)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, tw.Add(ik(t, e.userKey, e.seq, keys.KindPut), []byte("v")))
	}
	require.NoError(t, tw.Finish())

	tbl, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = tbl.Close() }()

	require.Equal(t, uint64(9999), tbl.MaxSeq(),
		"footer maxSeq must be the max over every entry, not the seq of the last key")
}

// Same invariant with the maximum in the middle of the key range, and spread
// across several blocks so the index has more than one entry to be misled by.
func TestFooterMaxSeqAcrossManyBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(2))
	tw, err := NewWriter(path)
	require.NoError(t, err)

	const n = 200
	const peakAt = n / 2
	want := uint64(0)
	for i := range n {
		seq := uint64(i + 1)
		if i == peakAt {
			seq = 1 << 40
		}
		want = max(want, seq)
		require.NoError(t, tw.Add(
			ik(t, fmt.Sprintf("key%04d", i), seq, keys.KindPut),
			make([]byte, 64),
		))
	}
	require.NoError(t, tw.Finish())

	tbl, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = tbl.Close() }()

	idxEntries := 0
	for it := (&blockIter{b: tbl.index}); it.Valid(); it.Next() {
		idxEntries++
	}
	require.Greater(t, idxEntries, 1, "input must span several blocks for this test to mean anything")
	require.Equal(t, want, tbl.MaxSeq())
}

// A seq above the trailer's ceiling could never have been written legally, so a
// footer claiming one is corrupt. Without the bound, Open hands that value to
// the DB, which adds 1 and hands out seqnums from an arbitrary point — or wraps
// to 0 and starts reusing numbers that live data already holds.
func TestFooterMaxSeqAboveCeilingIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(3))
	tw, err := NewWriter(path)
	require.NoError(t, err)
	require.NoError(t, tw.Add(ik(t, "k", 1, keys.KindPut), []byte("v")))
	require.NoError(t, tw.Finish())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	footer := raw[uint64(len(raw))-footerSize:]
	binary.LittleEndian.PutUint64(footer[16:24], keys.MaxSeq+1)

	bad := filepath.Join(t.TempDir(), FileName(4))
	require.NoError(t, os.WriteFile(bad, raw, 0o644))

	_, err = Open(bad)
	require.Error(t, err, "a footer seq above keys.MaxSeq must not open")
}

// The magic is the version gate and it lives in the last 8 bytes of the file,
// at a fixed offset from EOF, in every format version. A v1 table (24-byte
// footer, no maxSeq) must be rejected rather than read with a garbage seq.
func TestV1TableIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(5))
	tw, err := NewWriter(path)
	require.NoError(t, err)
	require.NoError(t, tw.Add(ik(t, "k", 1, keys.KindPut), []byte("v")))
	require.NoError(t, tw.Finish())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// Rebuild the tail as a v1 footer: indexHandle | magic, no maxSeq.
	body := raw[:uint64(len(raw))-footerSize]
	v1Footer := make([]byte, 24)
	copy(v1Footer[0:16], raw[uint64(len(raw))-footerSize:][0:16])
	binary.LittleEndian.PutUint64(v1Footer[16:24], 0x0100004D534C7A4F)

	old := filepath.Join(t.TempDir(), FileName(6))
	require.NoError(t, os.WriteFile(old, append(body, v1Footer...), 0o644))

	_, err = Open(old)
	require.ErrorIs(t, err, ErrBadMagic, "a v1 table must be rejected by the magic check")
}
