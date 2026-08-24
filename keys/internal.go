package keys

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

const (
	TrailerSize = 8
	MaxSeq      = 1<<56 - 1
)

var (
	ErrSeqOverflow  = errors.New("keys: seq overflows 56 bits")
	ErrUnknownKind  = errors.New("keys: unknown kind")
	ErrKeyTooShort  = errors.New("keys: internal key too short")
	ErrEmptyUserKey = errors.New("keys: empty user key")
)

type LookupState uint8

const (
	Found LookupState = iota
	Deleted
	NotFound
)

// Kind occupies the low byte of the trailer, so it must fit in 8 bits.
type Kind uint8

const (
	KindPut    Kind = iota // key → value
	KindDelete             // tombstone: key present, value empty
)

func (k Kind) String() string {
	switch k {
	case KindPut:
		return "put"
	case KindDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// Encode writes userKey bytes, then an 8-byte little-endian trailer (seq<<8 | kind).
func Encode(userKey []byte, seq uint64, kind Kind) ([]byte, error) {
	if len(userKey) == 0 {
		return nil, ErrEmptyUserKey
	}
	if seq > MaxSeq {
		return nil, ErrSeqOverflow
	}

	if kind != KindPut && kind != KindDelete {
		return nil, ErrUnknownKind
	}
	buf := make([]byte, len(userKey)+TrailerSize)
	copy(buf, userKey)
	binary.LittleEndian.PutUint64(buf[len(userKey):], seq<<8|uint64(kind))

	// The trailer is the whole ordering scheme: assert it reads back as what went
	// in, so a shift or mask regression dies here instead of as a mis-sorted
	// SSTable a thousand writes later. Encode runs on every write and Ensure
	// boxes its arguments into []any whether or not it fires — a %x on the key
	// plus %d on the trailer fields measured 5 allocs and 96 B per call — so the
	// message stays literal. The values are on the stack at the panic.
	if contract.Enabled {
		gotKey, gotSeq, gotKind, decErr := Decode(buf)
		ok := decErr == nil && bytes.Equal(gotKey, userKey) && gotSeq == seq && gotKind == kind
		contract.Ensure(ok, "keys.Encode: trailer does not round-trip")
	}
	return buf, nil
}

// Decode returns userKey (aliasing ik, not a copy), seq, and kind.
func Decode(ik []byte) (userKey []byte, seq uint64, kind Kind, err error) {
	if len(ik) < TrailerSize {
		return nil, 0, 0, ErrKeyTooShort
	}
	userKey = ik[:len(ik)-TrailerSize]
	trailer := binary.LittleEndian.Uint64(ik[len(userKey):])
	seq = trailer >> 8
	kind = Kind(trailer & 0xff)
	return userKey, seq, kind, nil
}
