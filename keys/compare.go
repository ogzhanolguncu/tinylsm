package keys

import (
	"bytes"
	"encoding/binary"

	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

// Compare returns +1 if a > b and returns -1 when a < b. This happens only when the keys are different. It basically lexicographically sorts.
// If keys are the same it compares seq numbers and if seqA > seqB it returns -1 and +1 when seqA < seqB. Returns 0 if seqs are same.
func Compare(a, b []byte) int {
	contract.Require(len(a) >= trailerSize, "keys.Compare: a is %d bytes, an internal key needs at least %d", len(a), trailerSize)
	contract.Require(len(b) >= trailerSize, "keys.Compare: b is %d bytes, an internal key needs at least %d", len(b), trailerSize)
	aUserKey := a[:len(a)-trailerSize]
	trailer := binary.LittleEndian.Uint64(a[len(aUserKey):])
	seqA := trailer >> 8

	bUserKey := b[:len(b)-trailerSize]
	bTrailer := binary.LittleEndian.Uint64(b[len(bUserKey):])
	seqB := bTrailer >> 8

	if bytes.Equal(aUserKey, bUserKey) {
		if seqA > seqB {
			return -1
		} else if seqA < seqB {
			return 1
		}
		return 0
	}

	return bytes.Compare(aUserKey, bUserKey)
}
