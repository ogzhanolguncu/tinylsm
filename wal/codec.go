package wal

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

const (
	// frame offsets — crc + len header, not covered by CRC
	offCRC     = 0          // uint32, 4 bytes
	offLen     = offCRC + 4 // uint32, 4 bytes
	headerSize = offLen + 4 // payload starts here (= 8)

	// payload-relative offsets — CRC covers all of payload
	pOffSeq    = 0            // uint64, 8 bytes
	pOffKind   = pOffSeq + 8  // byte,   1 byte
	pOffKeyLen = pOffKind + 1 // varint begins — last fixed offset (= 9)
)

const minRecordSize = headerSize + pOffKeyLen + 2 // two 1-byte varints: keyLen=0, valLen=0

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

func encode(entry Entry) ([]byte, error) {
	key, value, kind, seq := entry.key, entry.value, entry.kind, entry.seq

	if kind != KindPut && kind != KindDelete {
		return nil, ErrInvalidInput
	}
	if kind == KindDelete && len(value) > 0 {
		return nil, ErrInvalidInput
	}

	p := make([]byte, 0, pOffKeyLen+sizeUvarint(uint64(len(key)))+len(key)+sizeUvarint(uint64(len(value)))+len(value))
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

// decode parses one framed record from rec
// zero-copy, Entry.key and Entry.value alias rec's memory.
// They are valid only until caller modifies or reuses rec
// Callers that retain the Entry should copy to be safe
func decode(rec []byte) (Entry, error) {
	if len(rec) < minRecordSize {
		return Entry{}, ErrTruncated
	}

	crc := binary.LittleEndian.Uint32(rec[offCRC:offLen])
	length := binary.LittleEndian.Uint32(rec[offLen:headerSize])

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

	seq := binary.LittleEndian.Uint64(payload[pOffSeq:pOffKind])
	kind := Kind(payload[pOffKind])
	if kind > KindDelete {
		return Entry{}, ErrMalformed
	}
	off := pOffKeyLen

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
