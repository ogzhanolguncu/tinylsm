package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
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

func TestBlockBuilderRejectsOutOfOrderKeys(t *testing.T) {
	if !contract.Enabled {
		t.Skip("contracts compiled out")
	}

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

// Reset truncates lastKey, and keys.Compare panics on keys shorter than the
// trailer — so Add's contract must short-circuit on Empty() before reaching
// Compare. Cross-block ordering is the table writer's job, not the builder's.
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

	// Grow past the initial capacity so a Reset that reallocates lands on a
	// different cap than one that reuses the buffer.
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
