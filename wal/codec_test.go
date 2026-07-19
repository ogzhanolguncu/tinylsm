package wal

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		val  []byte
		seq  uint64
		kind Kind
	}{
		{"basic", []byte("cat"), []byte("purr"), 7, KindPut},
		{"empty value (tombstone)", []byte("cat"), nil, 42, KindDelete},
		{"empty key", nil, []byte("orphan"), 1, KindPut},
		{"both empty (min record)", nil, nil, 0, KindPut},
		{"long key forces 2-byte varint", bytes.Repeat([]byte("k"), 200), []byte("v"), 9, KindPut},
		{"long value forces 2-byte varint", []byte("k"), bytes.Repeat([]byte("v"), 300), 9, KindPut},
		{"binary data with zero bytes", []byte{0x00, 0xFF, 0x00}, []byte{0x00, 0x00}, 1 << 60, KindPut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mustEncode(t, tc.key, tc.val, tc.seq, tc.kind)
			e, _, err := decode(rec)
			require.NoError(t, err, "decode")
			require.Equal(t, string(tc.key), string(e.key), "key")
			require.Equal(t, string(tc.val), string(e.value), "value")
			require.Equal(t, tc.seq, e.seq, "seq")
			require.Equal(t, tc.kind, e.kind, "kind")
		})
	}
}

// Every prefix of a valid record is a torn write. Decode must return an
// error for all of them and panic for none.
func TestTruncatedPrefixes(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	for i := 0; i < len(rec); i++ {
		_, _, err := decode(rec[:i])
		require.Errorf(t, err, "decode(rec[:%d])", i)
	}
}

// Trailing bytes after a record are the next record, not corruption.
func TestTrailingBytesIgnored(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	buf := append(append([]byte{}, rec...), []byte("garbage that is really the next record")...)
	e, _, err := decode(buf)
	require.NoError(t, err, "decode with trailing bytes")
	require.Equal(t, "cat", string(e.key))
	require.Equal(t, "purr", string(e.value))
}

// Flip each byte in turn: every flip must be rejected. Flips inside the
// payload must be caught specifically by the checksum.
func TestSingleBitCorruption(t *testing.T) {
	orig := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	for i := 0; i < len(orig); i++ {
		rec := append([]byte{}, orig...)
		rec[i] ^= 0xFF
		_, _, err := decode(rec)
		require.Errorf(t, err, "byte %d flipped: want error", i)
		if i >= headerSize {
			require.ErrorIsf(t, err, ErrChecksum, "payload byte %d flipped", i)
		}
	}
}

// length field lies small: buffer is intact, claim describes a payload too
// short to hold the fixed prefix. CRC is recomputed over the lying span so
// the structural guard — not the checksum — must reject it.
func TestLengthLiesSmall(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	binary.LittleEndian.PutUint32(rec[offLen:headerSize], 5)
	binary.LittleEndian.PutUint32(rec[offCRC:offLen], crc32.Checksum(rec[headerSize:headerSize+5], Castagnoli))
	_, _, err := decode(rec)
	require.ErrorIs(t, err, ErrMalformed)
}

// keyLen claims more bytes than the payload holds, checksum valid.
func TestKeyLenLies(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, byte(KindPut))
	p = binary.AppendUvarint(p, 200) // keyLen claims 200...
	p = append(p, 'x')               // ...one byte follows
	_, _, err := decode(frame(p))
	require.ErrorIs(t, err, ErrMalformed)
}

// Payload ends in the middle of a varint (continuation bit set, no
// terminator). Uvarint reports n == 0; decode must not loop or accept.
func TestIncompleteVarint(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, byte(KindPut))
	p = append(p, 0x80, 0x80) // varint never terminates
	_, _, err := decode(frame(p))
	require.ErrorIs(t, err, ErrMalformed)
}

// Decode is strict: a kind no encoder produces is corruption, and replaying
// an operation we don't understand is worse than stopping. Format evolution
// belongs in a version field, not in tolerated mystery bytes.
func TestUnknownKindRejected(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, 0xFF) // kind no encoder would produce
	p = binary.AppendUvarint(p, 1)
	p = append(p, 'k')
	p = binary.AppendUvarint(p, 1)
	p = append(p, 'v')
	_, _, err := decode(frame(p))
	require.ErrorIs(t, err, ErrMalformed)
}

// The consumed count is what the reader uses to find record N+1: decode
// record, advance by n, decode again. If n is short (e.g. payload length
// without the crc+len header), the second decode lands mid-record.
func TestConsumedAdvancesToNextRecord(t *testing.T) {
	r1 := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	r2 := mustEncode(t, []byte("dog"), []byte("woof"), 8, KindPut)
	buf := append(append([]byte{}, r1...), r2...)

	e1, n1, err := decode(buf)
	require.NoError(t, err, "first record")
	require.Equal(t, "cat", string(e1.key))
	require.Equal(t, len(r1), n1, "consumed must equal full first record size")

	e2, n2, err := decode(buf[n1:])
	require.NoError(t, err, "second record, starting at consumed offset")
	require.Equal(t, "dog", string(e2.key))
	require.Equal(t, "woof", string(e2.value))
	require.Equal(t, len(r2), n2)
	require.Equal(t, len(buf), n1+n2, "two records consume the whole buffer")
}

// Tombstones must not carry a value; encode is the write-side gate.
func TestEncodeRejectsTombstoneWithValue(t *testing.T) {
	_, err := encode(Entry{key: []byte("cat"), value: []byte("oops"), seq: 7, kind: KindDelete})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func mustEncode(t *testing.T, key, val []byte, seq uint64, kind Kind) []byte {
	t.Helper()
	rec, err := encode(Entry{key: key, value: val, seq: seq, kind: kind})
	require.NoError(t, err, "encode")
	return rec
}

// frame wraps a hand-built payload with a valid crc+len header, so tests
// can craft structurally-broken payloads that pass the checksum.
func frame(p []byte) []byte {
	rec := binary.LittleEndian.AppendUint32(nil, crc32.Checksum(p, Castagnoli))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	return append(rec, p...)
}

// Decode must never panic, whatever bytes arrive.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	if rec, err := encode(Entry{key: []byte("cat"), value: []byte("purr"), seq: 7, kind: KindPut}); err == nil {
		f.Add(rec)
	}
	f.Add(frame([]byte{0x80, 0x80, 0x80}))
	f.Fuzz(func(t *testing.T, data []byte) {
		decode(data) // any error is fine; a panic fails the fuzz run
	})
}

// Whatever goes in must come out.
func FuzzRoundtrip(f *testing.F) {
	f.Add([]byte("cat"), []byte("purr"), uint64(7), byte(1))
	f.Add([]byte{}, []byte{}, uint64(0), byte(0))
	f.Fuzz(func(t *testing.T, key, val []byte, seq uint64, kindByte byte) {
		kind := Kind(kindByte % 2) // only legal kinds reach encode
		rec, err := encode(Entry{key: key, value: val, seq: seq, kind: kind})
		if kind == KindDelete && len(val) > 0 {
			require.ErrorIs(t, err, ErrInvalidInput, "tombstone with value")
			return
		}
		require.NoError(t, err, "encode")
		e, _, err := decode(rec)
		require.NoError(t, err, "decode of freshly encoded record")
		require.Equal(t, string(key), string(e.key), "key")
		require.Equal(t, string(val), string(e.value), "value")
		require.Equal(t, seq, e.seq, "seq")
		require.Equal(t, kind, e.kind, "kind")
	})
}
