package sstable

import (
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// ik is a test helper: encode an internal key or fail the test.
func ik(t *testing.T, userKey string, seq uint64, kind keys.Kind) []byte {
	t.Helper()
	k, err := keys.Encode([]byte(userKey), seq, kind)
	require.NoError(t, err)
	return k
}

func TestBlockBuilderExactBytes(t *testing.T) {
	// One entry, smallest interesting case. Compute the expected bytes BY HAND
	// from the format — do not compute them by running your own encoder, or the
	// test proves nothing.
	//
	//   entry := keyLen (varint) | internalKey | valLen (varint) | value
	//   block := entry* | crc (4)
	//
	// internalKey for ("a", seq=1, KindPut) is 1 + 8 = 9 bytes, so keyLen is
	// the single varint byte 0x09. Work out the trailer bytes from your
	// keys.Encode layout: (seq << 8) | kind, little-endian uint64.

	b := newBlockBuilder()
	b.Add(ik(t, "a", 1, keys.KindPut), []byte("v"))
	got := b.Finish()

	want := []byte{
		// TODO: keyLen
		// TODO: internalKey (9 bytes)
		// TODO: valLen
		// TODO: value
		// TODO: crc32c over everything above, little-endian
	}
	require.Equal(t, want, got)
}

func TestBlockBuilderChecksumVerifies(t *testing.T) {
	// Independent of the exact-bytes test: build a multi-entry block, split off
	// the trailer, recompute the CRC over the entries region, assert it matches
	// the stored one.
	t.Skip("TODO")
}

func TestBlockBuilderFullAndEmpty(t *testing.T) {
	// Empty() true before any Add, false after.
	// Full() false on a fresh builder; true once you have pushed past
	// targetBlockSize. Assert the boundary you actually chose in Full().
	t.Skip("TODO")
}

func TestBlockBuilderResetReuse(t *testing.T) {
	// Build block A, Reset, build block B with different entries. Assert B's
	// bytes contain no trace of A — this catches "Reset forgot the length
	// counter" and "Reset forgot lastKey".
	t.Skip("TODO")
}
