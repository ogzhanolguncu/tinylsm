package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
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

func writeTable(t *testing.T, path string, n int) {
	t.Helper()
	tw, err := newTableWriter(path)
	require.NoError(t, err)
	for i := range n {
		require.NoError(t, tw.Add(
			ik(t, fmt.Sprintf("key%04d", i), uint64(i+1), keys.KindPut),
			bytes.Repeat([]byte{byte(i)}, 512)))
	}
	require.NoError(t, tw.Finish())
}

// parseTable is the hand-rolled reader; openTable must agree with it exactly.
func TestOpenTableMatchesHandParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(3))
	writeTable(t, path, 40)

	want, _, _ := parseTable(t, path)
	require.GreaterOrEqual(t, len(want), 3, "input must span several blocks")

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	var got []indexEntry
	for it := (&blockIter{b: tbl.index}); it.Valid(); it.Next() {
		v := it.Value()
		require.Len(t, v, 16)
		got = append(got, indexEntry{
			lastKey: bytes.Clone(it.Key()),
			off:     binary.LittleEndian.Uint64(v[0:8]),
			size:    binary.LittleEndian.Uint64(v[8:16]),
		})
	}
	require.Equal(t, want, got)
}

func TestOpenTableRejectsCorruption(t *testing.T) {
	footerAt := func(raw []byte) []byte { return raw[uint64(len(raw))-footerSize:] }

	tests := []struct {
		name   string
		mangle func(raw []byte) []byte
		want   error
	}{
		{"shorter than footer", func(raw []byte) []byte {
			return raw[:footerSize-1]
		}, ErrBlockCorrupt},
		{"bad magic", func(raw []byte) []byte {
			footerAt(raw)[23] ^= 0xff
			return raw
		}, ErrBadMagic},
		{"indexSize overruns file", func(raw []byte) []byte {
			binary.LittleEndian.PutUint64(footerAt(raw)[8:16], uint64(len(raw)))
			return raw
		}, ErrBlockCorrupt},
		{"indexOff overruns file", func(raw []byte) []byte {
			binary.LittleEndian.PutUint64(footerAt(raw)[0:8], uint64(len(raw)))
			return raw
		}, ErrBlockCorrupt},
		{"index block bit flip", func(raw []byte) []byte {
			indexOff := binary.LittleEndian.Uint64(footerAt(raw)[0:8])
			raw[indexOff] ^= 0xff
			return raw
		}, ErrBlockChecksum},
	}

	golden := filepath.Join(t.TempDir(), FileName(1))
	writeTable(t, golden, 40)
	raw, err := os.ReadFile(golden)
	require.NoError(t, err)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), FileName(2))
			require.NoError(t, os.WriteFile(bad, tc.mangle(bytes.Clone(raw)), 0o644))

			tbl, err := openTable(bad)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, tbl)
		})
	}
}

type rec struct {
	userKey string
	seq     uint64
	kind    keys.Kind
	val     []byte
}

func buildTable(t *testing.T, path string, recs []rec) {
	t.Helper()
	tw, err := newTableWriter(path)
	require.NoError(t, err)
	for _, r := range recs {
		require.NoError(t, tw.Add(ik(t, r.userKey, r.seq, r.kind), r.val))
	}
	require.NoError(t, tw.Finish())
}

// snapshot is the seq a reader asks with: newer than anything in the table, so
// every version is visible. This is how the read path calls Get.
const snapshot = 1000

func TestTableGet(t *testing.T) {
	// 512-byte values so 40 records span several data blocks. A lookup that
	// picks the wrong index entry then reads the wrong block and misses.
	recs := make([]rec, 40)
	for i := range recs {
		recs[i] = rec{fmt.Sprintf("key%04d", i), uint64(i + 1), keys.KindPut, bytes.Repeat([]byte{byte(i)}, 512)}
	}
	recs[17].kind, recs[17].val = keys.KindDelete, nil

	path := filepath.Join(t.TempDir(), FileName(1))
	buildTable(t, path, recs)

	idx, _, _ := parseTable(t, path)
	require.GreaterOrEqual(t, len(idx), 3, "input must span several blocks")

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	tests := []struct {
		name    string
		userKey string
		want    LookupState
		wantVal []byte
	}{
		{"first key", "key0000", Found, recs[0].val},
		{"key in a later block", "key0025", Found, recs[25].val},
		{"last key", "key0039", Found, recs[39].val},
		{"tombstone", "key0017", Deleted, nil},
		{"absent, before every key", "aaa", NotFound, nil},
		{"absent, past every key", "zzz", NotFound, nil},
		{"absent, sorts inside a middle block", "key0025x", NotFound, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			val, st, err := tbl.Get(ik(t, tc.userKey, snapshot, keys.KindPut))
			require.NoError(t, err)
			require.Equal(t, tc.want, st)
			if tc.wantVal == nil {
				require.Nil(t, val)
				return
			}
			require.Equal(t, tc.wantVal, val)
		})
	}
}

// One user key, three versions. Get must return the newest version at or below
// the asked seq — the rule every snapshot read depends on.
func TestTableGetHonoursSnapshotSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName(2))
	buildTable(t, path, []rec{
		{"k", 9, keys.KindPut, []byte("v9")},
		{"k", 5, keys.KindPut, []byte("v5")},
		{"k", 1, keys.KindPut, []byte("v1")},
	})

	tbl, err := openTable(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	tests := []struct {
		seq     uint64
		want    LookupState
		wantVal string
	}{
		{10, Found, "v9"},
		{9, Found, "v9"},
		{7, Found, "v5"},
		{2, Found, "v1"},
		{0, NotFound, ""},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("seq%d", tc.seq), func(t *testing.T) {
			val, st, err := tbl.Get(ik(t, "k", tc.seq, keys.KindPut))
			require.NoError(t, err)
			require.Equal(t, tc.want, st)
			if tc.want == Found {
				require.Equal(t, []byte(tc.wantVal), val)
			}
		})
	}
}

// A damaged table must report an error, never NotFound: the read path takes
// NotFound as "ask the next table" and would answer from stale data.
func TestTableGetRejectsCorruption(t *testing.T) {
	t.Run("data block bit flip", func(t *testing.T) {
		golden := filepath.Join(t.TempDir(), FileName(3))
		writeTable(t, golden, 40)
		raw, err := os.ReadFile(golden)
		require.NoError(t, err)
		raw[0] ^= 0xff // first byte of data block 0; index and footer stay valid

		bad := filepath.Join(t.TempDir(), FileName(4))
		require.NoError(t, os.WriteFile(bad, raw, 0o644))

		tbl, err := openTable(bad)
		require.NoError(t, err, "only the data block is damaged")
		t.Cleanup(func() { require.NoError(t, tbl.Close()) })

		_, st, err := tbl.Get(ik(t, "key0000", snapshot, keys.KindPut))
		require.ErrorIs(t, err, ErrBlockChecksum)
		require.NotEqual(t, Found, st)
	})

	t.Run("unknown kind", func(t *testing.T) {
		// keys.Encode rejects undefined kinds, so this cannot be written through
		// the writer: patch the kind byte on disk and repair the block checksum,
		// which is what silent bit rot in a durable file looks like.
		dir := t.TempDir()
		golden := filepath.Join(dir, FileName(5))
		buildTable(t, golden, []rec{{"k", 1, keys.KindPut, []byte("v")}})

		idx, raw, _ := parseTable(t, golden)
		size := idx[0].size

		// data block 0 is one entry: keyLen varint | internalKey | valLen | value.
		// The kind is the low byte of the internal key's 8-byte trailer.
		keyLen, n := binary.Uvarint(raw)
		raw[uint64(n)+keyLen-keys.TrailerSize] = 2
		binary.LittleEndian.PutUint32(raw[size-blockTrailerSize:size],
			crc32.Checksum(raw[:size-blockTrailerSize], castagnoli))

		bad := filepath.Join(dir, FileName(6))
		require.NoError(t, os.WriteFile(bad, raw, 0o644))

		tbl, err := openTable(bad)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, tbl.Close()) })

		_, st, err := tbl.Get(ik(t, "k", snapshot, keys.KindPut))
		require.ErrorIs(t, err, ErrBlockCorrupt)
		require.NotEqual(t, Found, st)
	})
}

// A corrupt index entry can point a data block outside the file. The handle
// itself is well-formed and the index block's CRC is repaired, so nothing
// upstream of readBlock notices — only readBlock's span guard stands here.
func TestTableGetRejectsOutOfRangeBlockHandle(t *testing.T) {
	golden := filepath.Join(t.TempDir(), FileName(6))
	writeTable(t, golden, 40)
	raw, err := os.ReadFile(golden)
	require.NoError(t, err)

	footer := raw[uint64(len(raw))-footerSize:]
	indexOff := binary.LittleEndian.Uint64(footer[0:8])
	indexSize := binary.LittleEndian.Uint64(footer[8:16])
	idx := raw[indexOff : indexOff+indexSize]
	body := idx[:len(idx)-4]

	// entry layout: keyLen uvarint | key | valLen uvarint | val
	keyLen, n := binary.Uvarint(body)
	require.Greater(t, n, 0)
	pos := n + int(keyLen)
	valLen, n := binary.Uvarint(body[pos:])
	require.Equal(t, uint64(blockHandleSize), valLen)
	pos += n

	binary.LittleEndian.PutUint64(body[pos:pos+8], uint64(len(raw)))
	binary.LittleEndian.PutUint32(idx[len(idx)-4:], crc32.Checksum(body, castagnoli))

	bad := filepath.Join(t.TempDir(), FileName(7))
	require.NoError(t, os.WriteFile(bad, raw, 0o644))

	tbl, err := openTable(bad)
	require.NoError(t, err, "the index block itself is still well-formed")
	t.Cleanup(func() { require.NoError(t, tbl.Close()) })

	_, st, err := tbl.Get(ik(t, "key0000", snapshot, keys.KindPut))
	require.ErrorIs(t, err, ErrBlockCorrupt)
	require.NotEqual(t, Found, st)
}

// A footer handle can point at a real, well-formed block that is simply the
// wrong one — a data block instead of the index. The span guard passes and the
// CRC passes, because those bytes ARE a valid block. Only the rule "the index
// ends exactly where the footer begins" rejects it.
func TestOpenTableRejectsIndexHandlePointingAtDataBlock(t *testing.T) {
	golden := filepath.Join(t.TempDir(), FileName(8))
	writeTable(t, golden, 40)

	idx, raw, _ := parseTable(t, golden)
	require.GreaterOrEqual(t, len(idx), 3, "input must span several blocks")

	footer := raw[uint64(len(raw))-footerSize:]
	binary.LittleEndian.PutUint64(footer[0:8], idx[0].off)
	binary.LittleEndian.PutUint64(footer[8:16], idx[0].size)

	bad := filepath.Join(t.TempDir(), FileName(9))
	require.NoError(t, os.WriteFile(bad, raw, 0o644))

	tbl, err := openTable(bad)
	require.ErrorIs(t, err, ErrBlockCorrupt)
	require.Nil(t, tbl)
}
