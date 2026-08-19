package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

func ik(t *testing.T, userKey string, seq uint64, kind keys.Kind) []byte {
	t.Helper()
	k, err := keys.Encode([]byte(userKey), seq, kind)
	require.NoError(t, err)
	return k
}

func TestBlockBuilderExactBytes(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "a", 1, keys.KindPut), []byte("v"))

	want := []byte{
		0x09,                                           // keyLen varint
		0x61,                                           // userKey "a"
		0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // trailer (1<<8)|0, LE
		0x01,                   // valLen varint
		0x76,                   // value "v"
		0x80, 0x45, 0x28, 0x7b, // crc32c of the 12 bytes above, LE
	}
	require.Equal(t, want, b.Finish())
}

// Add's contract must short-circuit on Empty() before reaching Compare, which
// panics on the zero-length lastKey Reset leaves behind.
func TestBlockBuilderAddAfterResetDoesNotPanic(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "b", 1, keys.KindPut), []byte("v1"))
	_ = b.Finish()
	b.Reset()

	require.NotPanics(t, func() { b.Add(ik(t, "a", 1, keys.KindPut), []byte("v2")) })
}

func TestBlockBuilderChecksumVerifies(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "apple", 3, keys.KindPut), []byte("red"))
	b.Add(ik(t, "banana", 2, keys.KindPut), []byte("yellow"))
	b.Add(ik(t, "cherry", 1, keys.KindDelete), nil)
	block := b.Finish()

	require.Greater(t, len(block), blockTrailerSize)
	entries := block[:len(block)-blockTrailerSize]
	stored := binary.LittleEndian.Uint32(block[len(block)-blockTrailerSize:])

	require.Equal(t, crc32.Checksum(entries, castagnoli), stored)
}

func TestBlockBuilderFullAndEmpty(t *testing.T) {
	b := newBlockBuilder()
	require.True(t, b.Empty())
	require.False(t, b.Full())

	val := bytes.Repeat([]byte("v"), 512)
	b.Add(ik(t, "key000", 1, keys.KindPut), val)
	require.False(t, b.Empty())
	require.False(t, b.Full())

	for i := 1; !b.Full(); i++ {
		b.Add(ik(t, fmt.Sprintf("key%03d", i), 1, keys.KindPut), val)
		require.Less(t, i, 100, "Full never tripped")
	}
	require.GreaterOrEqual(t, len(b.buf), targetBlockSize)

	// Full uses >=, so it stays true as the block overshoots.
	b.Add(ik(t, "zzz", 1, keys.KindPut), val)
	require.True(t, b.Full())
}

func TestBlockBuilderResetReuse(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "apple", 1, keys.KindPut), []byte("red"))

	// Grow past the initial cap so realloc and reuse give different caps.
	val := bytes.Repeat([]byte("v"), 512)
	for i := 0; !b.Full(); i++ {
		b.Add(ik(t, fmt.Sprintf("filler%03d", i), 1, keys.KindPut), val)
	}
	blockA := bytes.Clone(b.Finish())
	capBefore := cap(b.buf)
	require.Greater(t, capBefore, targetBlockSize, "buffer must have grown")

	b.Reset()
	require.True(t, b.Empty())
	require.Empty(t, b.lastKey, "Reset must clear lastKey")

	b.Add(ik(t, "banana", 2, keys.KindPut), []byte("yellow"))
	blockB := b.Finish()

	fresh := newBlockBuilder()
	fresh.Add(ik(t, "banana", 2, keys.KindPut), []byte("yellow"))

	require.Equal(t, fresh.Finish(), blockB, "reused builder must match a fresh one")
	require.NotContains(t, string(blockB), "apple")
	require.NotEqual(t, blockA, blockB)
	require.Equal(t, capBefore, cap(b.buf), "Reset must retain the allocation")
}

func TestNewBlockRoundTrip(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "a", 2, keys.KindPut), []byte("v1"))
	b.Add(ik(t, "b", 1, keys.KindDelete), nil) // tombstone: zero-length value

	blk, err := newBlock(b.Finish())
	require.NoError(t, err)

	// entry 0: 1 keyLen byte + 9 key + 1 valLen byte + 2 val = 13
	// entry 1: 1 + 9 + 1 + 0 = 11
	require.Equal(t, []int{0, 13}, blk.offsets)
	require.Len(t, blk.data, 24, "trailer must be sliced off")
}

func TestBlockIterWalksAllEntries(t *testing.T) {
	entries := []struct {
		key, val []byte
	}{
		{ik(t, "a", 3, keys.KindPut), []byte("v1")},
		{ik(t, "b", 2, keys.KindDelete), nil}, // tombstone: zero-length value
		{ik(t, "c", 1, keys.KindPut), bytes.Repeat([]byte("x"), 200)},
	}
	b := newBlockBuilder()
	for _, e := range entries {
		b.Add(e.key, e.val)
	}
	blk, err := newBlock(b.Finish())
	require.NoError(t, err)

	it := &blockIter{b: blk}
	for i, e := range entries {
		require.True(t, it.Valid(), "entry %d", i)
		require.Equal(t, e.key, it.Key(), "entry %d", i)
		if len(e.val) == 0 {
			require.Empty(t, it.Value(), "entry %d", i)
		} else {
			require.Equal(t, e.val, it.Value(), "entry %d", i)
		}
		it.Next()
	}
	require.False(t, it.Valid(), "iterator must be exhausted after last entry")
}

// newBlock and a full iteration must never panic, whatever bytes arrive.
// Raw input alone would only ever exercise the checksum branch (the fuzzer
// can't guess valid CRCs), so each input is also retried as a payload with
// a correct CRC appended, which sends arbitrary structure into the walk.
func FuzzBlockDecodeNeverPanics(f *testing.F) {
	b := newBlockBuilder()
	if k, err := keys.Encode([]byte("cat"), 7, keys.KindPut); err == nil {
		b.Add(k, []byte("purr"))
		f.Add(b.Finish())
	}
	f.Add([]byte{})
	f.Add([]byte{0x80})
	f.Add(binary.AppendUvarint(nil, 1<<30))
	f.Add(binary.AppendUvarint(nil, 1<<63))

	walk := func(data []byte) {
		blk, err := newBlock(data)
		if err != nil {
			return
		}
		it := &blockIter{b: blk}
		for it.Valid() {
			_ = it.Key()
			_ = it.Value()
			it.Next()
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		walk(data)
		crc := crc32.Checksum(data, castagnoli)
		walk(binary.LittleEndian.AppendUint32(bytes.Clone(data), crc))
	})
}

func TestNewBlockRejectsCorruption(t *testing.T) {
	mk := func(payload []byte) []byte {
		return binary.LittleEndian.AppendUint32(payload, crc32.Checksum(payload, castagnoli))
	}
	key := ik(t, "a", 1, keys.KindPut)
	keyPrefix := append(binary.AppendUvarint(nil, uint64(len(key))), key...)

	b := newBlockBuilder()
	b.Add(key, []byte("v"))
	flipped := bytes.Clone(b.Finish())
	flipped[0] ^= 0xFF

	cases := map[string]struct {
		block []byte
		want  error
	}{
		"nil":               {nil, ErrBlockCorrupt},
		"trailer only":      {mk(nil), ErrBlockCorrupt},
		"flipped byte":      {flipped, ErrBlockChecksum},
		"cut keyLen varint": {mk([]byte{0x80}), ErrBlockCorrupt},
		"key below trailer": {mk(binary.AppendUvarint(nil, 2)), ErrBlockCorrupt},
		"keyLen overrun":    {mk(binary.AppendUvarint(nil, 1<<30)), ErrBlockCorrupt},
		"missing valLen":    {mk(bytes.Clone(keyPrefix)), ErrBlockCorrupt},
		"valLen sign bit":   {mk(binary.AppendUvarint(bytes.Clone(keyPrefix), 1<<63)), ErrBlockCorrupt},
	}
	for name, tc := range cases {
		_, err := newBlock(tc.block)
		require.ErrorIs(t, err, tc.want, name)
	}
}

func TestBlockIterSeek(t *testing.T) {
	// Distinct user keys, one version each, so ordering is pure bytewise.
	b := newBlockBuilder()
	b.Add(ik(t, "a", 5, keys.KindPut), []byte("va"))
	b.Add(ik(t, "c", 5, keys.KindPut), []byte("vc"))
	b.Add(ik(t, "e", 5, keys.KindPut), []byte("ve"))
	blk, err := newBlock(b.Finish())
	require.NoError(t, err)
	require.Len(t, blk.offsets, 3)

	tests := []struct {
		name    string
		target  []byte
		wantIdx int
		wantKey string // "" means the iterator must be invalid
	}{
		{"exact match on first", ik(t, "a", 5, keys.KindPut), 0, "a"},
		{"exact match in middle", ik(t, "c", 5, keys.KindPut), 1, "c"},
		{"exact match on last", ik(t, "e", 5, keys.KindPut), 2, "e"},
		{"between keys lands on the next", ik(t, "b", 5, keys.KindPut), 1, "c"},
		{"before every key lands on the first", ik(t, "A", 5, keys.KindPut), 0, "a"},
		{"past the last key is invalid", ik(t, "z", 5, keys.KindPut), 3, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &blockIter{b: blk}
			it.Seek(tt.target)

			require.Equal(t, tt.wantIdx, it.i)
			if tt.wantKey == "" {
				require.False(t, it.Valid())
				return
			}
			require.True(t, it.Valid())
			require.Equal(t, ik(t, tt.wantKey, 5, keys.KindPut), it.Key())
			require.Equal(t, []byte("v"+tt.wantKey), it.Value())
		})
	}
}

// Same user key, many versions. Higher seq sorts first, so Seek(key, snapshot)
// must land on the newest version at or below snapshot — never on a newer one.
func TestBlockIterSeekPicksNewestVisibleVersion(t *testing.T) {
	b := newBlockBuilder()
	b.Add(ik(t, "k", 9, keys.KindPut), []byte("v9"))
	b.Add(ik(t, "k", 5, keys.KindPut), []byte("v5"))
	b.Add(ik(t, "k", 1, keys.KindPut), []byte("v1"))
	blk, err := newBlock(b.Finish())
	require.NoError(t, err)

	tests := []struct {
		snapshot uint64
		wantVal  string
	}{
		{100, "v9"}, // newer than everything
		{9, "v9"},   // exactly the newest
		{7, "v5"},   // between versions: v9 is invisible
		{5, "v5"},
		{3, "v1"},
		{1, "v1"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("snapshot_%d", tt.snapshot), func(t *testing.T) {
			it := &blockIter{b: blk}
			it.Seek(ik(t, "k", tt.snapshot, keys.KindPut))

			require.True(t, it.Valid())
			require.Equal(t, []byte(tt.wantVal), it.Value())
		})
	}
}
