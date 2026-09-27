package frame

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

var (
	ErrTruncated = errors.New("frame: truncated record")
	ErrChecksum  = errors.New("frame: checksum mismatch")
	ErrMalformed = errors.New("frame: malformed record")
)

const (
	// frame offsets — crc + len header, not covered by CRC
	OffCRC     = 0          // uint32, 4 bytes
	OffLen     = OffCRC + 4 // uint32, 4 bytes
	HeaderSize = OffLen + 4 // payload starts here (= 8)
)

var Castagnoli = crc32.MakeTable(crc32.Castagnoli)

func Frame(p []byte) []byte {
	rec := binary.LittleEndian.AppendUint32(nil, crc32.Checksum(p, Castagnoli))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	return append(rec, p...)
}

// frameSize reports the full record size (header + payload) the header at
// the front of rec claims, or false if rec holds no complete header.
func FrameSize(rec []byte) (int, bool) {
	if len(rec) < HeaderSize {
		return 0, false
	}

	n := binary.LittleEndian.Uint32(rec[OffLen:HeaderSize])
	return HeaderSize + int(n), true
}

func ValidateFrame(rec []byte) ([]byte, int, error) {
	if len(rec) < HeaderSize {
		return nil, 0, ErrTruncated
	}

	crc := binary.LittleEndian.Uint32(rec[OffCRC:OffLen])
	length := binary.LittleEndian.Uint32(rec[OffLen:HeaderSize])

	if int(length) > len(rec)-HeaderSize {
		return nil, 0, ErrTruncated
	}

	p := rec[HeaderSize : HeaderSize+length]

	if crc32.Checksum(p, Castagnoli) != crc {
		return nil, 0, ErrChecksum
	}
	return p, int(length), nil
}
