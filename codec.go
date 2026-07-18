package main

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/bits"
)

var (
	ErrTruncated    = errors.New("wal: truncated record")
	ErrChecksum     = errors.New("wal: checksum mismatch")
	ErrMalformed    = errors.New("wal: malformed record")
	ErrInvalidInput = errors.New("wal: invalid input")
)

const minRecordSize = offKeyLen + 2 // two 1-byte varints: keyLen=0, valLen=0

const (
	offCRC    = 0           // uint32, 4 bytes
	offLen    = offCRC + 4  // uint32, 4 bytes
	offSeq    = offLen + 4  // uint64, 8 bytes  (payload starts here)
	offKind   = offSeq + 8  // byte,   1 byte
	offKeyLen = offKind + 1 // varint begins — last fixed offset (= 17)

	headerSize = offSeq // crc+len; not covered by CRC (= 8)
	seqSize    = offKind - offSeq // 8
	kindSize   = offKeyLen - offKind

)

var Castagnoli = crc32.MakeTable(crc32.Castagnoli)

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

func encode(key, value []byte, seq uint64, kind Kind) ([]byte, error) {
	if kind != KindPut && kind != KindDelete {
		return nil, ErrInvalidInput
	}
	if kind == KindDelete && len(value) > 0 {
		return nil, ErrInvalidInput
	}

	p := make([]byte, 0, seqSize+kindSize+sizeUvarint(uint64(len(key)))+len(key)+sizeUvarint(uint64(len(value)))+len(value))
	p = binary.LittleEndian.AppendUint64(p, seq)
	p = append(p, byte(kind))
	p = binary.AppendUvarint(p, uint64(len(key)))
	p = append(p, key...)
	p = binary.AppendUvarint(p, uint64(len(value)))
	p = append(p, value...)

	crc := crc32.Checksum(p, Castagnoli)

	rec := make([]byte, 0, headerSize+len(p))
	rec = binary.LittleEndian.AppendUint32(rec, crc)
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	rec = append(rec, p...)

	return rec, nil
}

type Entry struct {
	key, value []byte
	seq        uint64
	kind       Kind
}

func decode(rec []byte) (Entry, error) {
	if len(rec) < minRecordSize {
		return Entry{}, ErrTruncated
	}

	crc := binary.LittleEndian.Uint32(rec[offCRC:offLen])
	length := binary.LittleEndian.Uint32(rec[offLen:offSeq])

	if int(length) < minRecordSize-headerSize {
		return Entry{}, ErrMalformed
	}
	if int(length) > len(rec)-headerSize {
		return Entry{}, ErrTruncated
	}
	payload := rec[headerSize : headerSize+length]

	if crc32.Checksum(payload, Castagnoli) != crc {
		return Entry{}, ErrChecksum
	}

	seq := binary.LittleEndian.Uint64(payload[offCRC:offSeq])
	kind := Kind(payload[offSeq])
	if kind > KindDelete {
		return Entry{}, ErrMalformed
	}
	off := 9

	keyLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, ErrMalformed
	}
	off += n

	if keyLen > uint64(len(payload)-off) {
		return Entry{}, ErrMalformed
	}
	key := payload[off : off+int(keyLen)]
	off += int(keyLen)

	valLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, ErrMalformed
	}
	if kind == KindDelete && valLen != 0 {
		return Entry{}, ErrMalformed
	}
	off += n

	if valLen > uint64(len(payload)-off) {
		return Entry{}, ErrMalformed
	}
	value := payload[off : off+int(valLen)]
	off += int(valLen)

	if off != int(length) {
		return Entry{}, ErrMalformed
	}

	return Entry{
		key:   key,
		value: value,
		seq:   seq,
		kind:  kind,
	}, nil
}

func sizeUvarint(x uint64) int {
	if x == 0 {
		return 1
	}
	return (bits.Len64(x) + 6) / 7
}
