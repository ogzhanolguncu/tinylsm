package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/bits"
)

type Entry struct {
	Key, Value []byte
	Seq        uint64
	Kind       Kind
}

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
	p, err := buildPayload(entry)
	if err != nil {
		return nil, fmt.Errorf("payload construction: %w", err)
	}
	return frame(p), nil
}

func buildPayload(entry Entry) ([]byte, error) {
	key, value, kind, seq := entry.Key, entry.Value, entry.Kind, entry.Seq

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
	return p, nil
}

func frame(p []byte) []byte {
	rec := binary.LittleEndian.AppendUint32(nil, crc32.Checksum(p, Castagnoli))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	return append(rec, p...)
}

// frameSize reports the full record size (header + payload) the header at
// the front of rec claims, or false if rec holds no complete header.
func frameSize(rec []byte) (int, bool) {
	if len(rec) < headerSize {
		return 0, false
	}

	n := binary.LittleEndian.Uint32(rec[offLen:headerSize])
	return headerSize + int(n), true
}

func validateFrame(rec []byte) ([]byte, int, error) {
	if len(rec) < minRecordSize {
		return nil, 0, ErrTruncated
	}

	crc := binary.LittleEndian.Uint32(rec[offCRC:offLen])
	length := binary.LittleEndian.Uint32(rec[offLen:headerSize])

	if int(length) < minRecordSize-headerSize {
		return nil, 0, ErrMalformed
	}
	if int(length) > len(rec)-headerSize {
		return nil, 0, ErrTruncated
	}

	p := rec[headerSize : headerSize+length]

	if crc32.Checksum(p, Castagnoli) != crc {
		return nil, 0, ErrChecksum
	}
	return p, int(length), nil
}

// decode parses one framed record from rec
// zero-copy, Entry.key and Entry.value alias rec's memory.
// They are valid only until caller modifies or reuses rec
// Callers that retain the Entry should copy to be safe
func decode(rec []byte) (Entry, int, error) {
	payload, length, err := validateFrame(rec)
	if err != nil {
		return Entry{}, 0, fmt.Errorf("validate frame: %w", err)
	}

	seq := binary.LittleEndian.Uint64(payload[pOffSeq:pOffKind])
	kind := Kind(payload[pOffKind])
	if kind > KindDelete {
		return Entry{}, 0, ErrMalformed
	}
	off := pOffKeyLen

	keyLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, 0, ErrMalformed
	}
	off += n
	// Payload size - bytes consumed so far
	rem := uint64(len(payload) - off)
	if keyLen > rem {
		return Entry{}, 0, ErrMalformed
	}
	key := payload[off : off+int(keyLen)]
	off += int(keyLen)

	valLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, 0, ErrMalformed
	}
	if kind == KindDelete && valLen != 0 {
		return Entry{}, 0, ErrMalformed
	}
	off += n

	rem = uint64(len(payload) - off)
	if valLen > rem {
		return Entry{}, 0, ErrMalformed
	}
	value := payload[off : off+int(valLen)]
	off += int(valLen)

	if off != int(length) {
		return Entry{}, 0, ErrMalformed
	}

	return Entry{
		Key:   key,
		Value: value,
		Seq:   seq,
		Kind:  kind,
	}, off + headerSize, nil
}

func sizeUvarint(x uint64) int {
	if x == 0 {
		return 1
	}
	return (bits.Len64(x) + 6) / 7
}
