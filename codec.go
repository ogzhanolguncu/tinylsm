package main

import (
	"encoding/binary"
	"hash/crc32"
	"math/bits"
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

func sizeUvarint(x uint64) int {
	if x == 0 {
		return 1
	}
	return (bits.Len64(x) + 6) / 7
}
