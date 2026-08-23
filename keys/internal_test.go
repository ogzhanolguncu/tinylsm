package keys

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every valid (userKey, seq, kind) must survive Encode -> Decode byte-for-byte.
// Binary keys and boundary seqs are the interesting cases: the format has no
// length prefix, so trailing key bytes must not be mistaken for the trailer,
// and seq must round-trip up to its 56-bit ceiling without bleeding into kind.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		userKey []byte
		seq     uint64
		kind    Kind
	}{
		{"put", []byte("cat"), 7, KindPut},
		{"delete tombstone", []byte("cat"), 8, KindDelete},
		{"single byte key", []byte("a"), 1, KindPut},
		{"binary key with 0x00 and 0xFF", []byte{0x00, 0xFF, 0x00, 0x80}, 42, KindPut},
		{"seq zero", []byte("k"), 0, KindPut},
		{"seq at 56-bit ceiling", []byte("k"), MaxSeq, KindDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ik, err := Encode(tc.userKey, tc.seq, tc.kind)
			require.NoError(t, err)
			require.Len(t, ik, len(tc.userKey)+TrailerSize)

			uk, seq, kind, err := Decode(ik)
			require.NoError(t, err)
			require.Equal(t, string(tc.userKey), string(uk), "userKey")
			require.Equal(t, tc.seq, seq, "seq")
			require.Equal(t, tc.kind, kind, "kind")
		})
	}
}

// The trailer packs seq into the top 56 bits and kind into the low byte. A
// max seq next to a nonzero kind is the case that catches an off-by-one in the
// shift/mask: neither field may corrupt the other.
func TestTrailerPackingIsolatesFields(t *testing.T) {
	ik, err := Encode([]byte("k"), MaxSeq, KindDelete)
	require.NoError(t, err)

	trailer := binary.LittleEndian.Uint64(ik[len(ik)-TrailerSize:])
	require.Equal(t, uint64(MaxSeq), trailer>>8, "seq occupies the top 56 bits")
	require.Equal(t, uint64(KindDelete), trailer&0xff, "kind occupies the low byte")

	_, seq, kind, err := Decode(ik)
	require.NoError(t, err)
	require.Equal(t, uint64(MaxSeq), seq)
	require.Equal(t, KindDelete, kind)
}

func TestEncodeRejectsEmptyUserKey(t *testing.T) {
	_, err := Encode(nil, 1, KindPut)
	require.ErrorIs(t, err, ErrEmptyUserKey, "nil user key")

	_, err = Encode([]byte{}, 1, KindPut)
	require.ErrorIs(t, err, ErrEmptyUserKey, "empty non-nil user key")
}

func TestEncodeRejectsSeqOverflow(t *testing.T) {
	_, err := Encode([]byte("k"), MaxSeq, KindPut)
	require.NoError(t, err, "MaxSeq is the largest legal seq")

	_, err = Encode([]byte("k"), MaxSeq+1, KindPut)
	require.ErrorIs(t, err, ErrSeqOverflow)
}

// Anything shorter than a bare trailer cannot hold a key + trailer.
func TestDecodeRejectsTooShort(t *testing.T) {
	for n := range TrailerSize {
		_, _, _, err := Decode(make([]byte, n))
		require.ErrorIsf(t, err, ErrKeyTooShort, "len %d must be rejected", n)
	}
}

// Decode aliases the input rather than copying: the returned userKey shares
// backing storage with ik. This is the documented zero-copy contract; the
// test pins it so a future switch to bytes.Clone is a conscious change.
func TestDecodeAliasesInput(t *testing.T) {
	ik, err := Encode([]byte("cat"), 7, KindPut)
	require.NoError(t, err)

	uk, _, _, err := Decode(ik)
	require.NoError(t, err)
	require.Equal(t, "cat", string(uk))

	ik[0] = 'b' // mutate the source
	require.Equal(t, "bat", string(uk), "userKey must alias ik, not copy it")
}

// The trailer has two bits of room for the kind, but only two kinds are
// defined. A kind nothing can interpret must not reach disk.
func TestEncodeRejectsUndefinedKinds(t *testing.T) {
	for _, k := range []Kind{2, 3, 4, 255} {
		_, err := Encode([]byte("a"), 1, k)
		require.ErrorIsf(t, err, ErrUnknownKind, "kind %d", k)
	}
}
