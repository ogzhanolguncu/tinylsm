package main

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/bits"
)

var (
	ErrTruncated = errors.New("wal: truncated record")
	ErrChecksum  = errors.New("wal: checksum mismatch")
	ErrMalformed = errors.New("wal: malformed record")
)

const (
	emptyKeyLen   = 1
	emptyValLen   = 1
	minRecordSize = offKeyLen + emptyKeyLen + emptyValLen
)

const (
	offCRC    = 0           // uint32, 4 bytes
	offLen    = offCRC + 4  // uint32, 4 bytes
	offSeq    = offLen + 4  // uint64, 8 bytes  (payload starts here)
	offKind   = offSeq + 8  // byte,   1 byte
	offKeyLen = offKind + 1 // varint begins — last fixed offset (= 17)

	headerSize  = offSeq // crc+len; not covered by CRC (= 8)
	payloadHead = offSeq // CRC covers from here to end
)

var Castagnoli = crc32.MakeTable(crc32.Castagnoli)

func encode(key, value []byte, seq uint64, kind byte) []byte {
	p := make([]byte, 0, 8+1+sizeUvarint(uint64(len(key)))+len(key)+sizeUvarint(uint64(len(value)))+len(value))
	p = binary.LittleEndian.AppendUint64(p, seq)
	p = append(p, kind)
	p = binary.AppendUvarint(p, uint64(len(key)))
	p = append(p, key...)
	p = binary.AppendUvarint(p, uint64(len(value)))
	p = append(p, value...)

	crc := crc32.Checksum(p, Castagnoli)

	rec := make([]byte, 0, headerSize+len(p))
	rec = binary.LittleEndian.AppendUint32(rec, crc)
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	rec = append(rec, p...)

	return rec
}

type Entry struct {
	key, value []byte
	seq        uint64
	kind       byte
}

func decode(rec []byte) (Entry, error) {
	if len(rec) < minRecordSize {
		return Entry{}, ErrTruncated
	}
	crc := binary.LittleEndian.Uint32(rec[offCRC:offLen])
	length := binary.LittleEndian.Uint32(rec[offLen:offSeq])
	if int(length) > len(rec)-headerSize {
		return Entry{}, ErrTruncated
	}
	payload := rec[headerSize : headerSize+length]

	if crc32.Checksum(payload, Castagnoli) != crc {
		return Entry{}, ErrChecksum
	}

	seq := binary.LittleEndian.Uint64(payload[0:8])
	kind := payload[8]

	off := 9
	keyLen, n := binary.Uvarint(payload[off:])
	off += n

	key := payload[off : off+int(keyLen)]
	off += int(keyLen)

	valLen, n := binary.Uvarint(payload[off:])
	off += n
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
