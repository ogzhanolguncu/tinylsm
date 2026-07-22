package keys

import (
	"encoding/binary"
	"errors"

	"github.com/ogzhanolguncu/tinylsm/wal"
)

const (
	trailerSize = 8
	maxSeq      = 1<<56 - 1
)

var (
	ErrSeqOverflow  = errors.New("keys: seq overflows 56 bits")
	ErrKindOverflow = errors.New("keys: kind overflows 8 bits")
	ErrKeyTooShort  = errors.New("keys: internal key too short")
	ErrEmptyUserKey = errors.New("keys: empty user key")
)

type Kind wal.Kind

const (
	KindPut    = Kind(wal.KindPut)
	KindDelete = Kind(wal.KindDelete)
)

// Encode writes userKey bytes, then an 8-byte little-endian trailer (seq<<8 | kind).
func Encode(userKey []byte, seq uint64, kind Kind) ([]byte, error) {
	if len(userKey) == 0 {
		return nil, ErrEmptyUserKey
	}
	if seq > maxSeq {
		return nil, ErrSeqOverflow
	}
	if uint64(kind) > 0x03 {
		return nil, ErrKindOverflow
	}
	buf := make([]byte, len(userKey)+trailerSize)
	copy(buf, userKey)
	binary.LittleEndian.PutUint64(buf[len(userKey):], seq<<8|uint64(kind))
	return buf, nil
}

// Decode returns userKey (aliasing ik, not a copy), seq, and kind.
func Decode(ik []byte) (userKey []byte, seq uint64, kind Kind, err error) {
	if len(ik) < trailerSize {
		return nil, 0, 0, ErrKeyTooShort
	}
	userKey = ik[:len(ik)-trailerSize]
	trailer := binary.LittleEndian.Uint64(ik[len(userKey):])
	seq = trailer >> 8
	kind = Kind(trailer & 0xff)
	return userKey, seq, kind, nil
}
