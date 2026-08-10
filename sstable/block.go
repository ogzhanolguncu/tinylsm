// Package sstable implements the on-disk sorted string table format.
//
// A block is the unit of I/O: a contiguous run of entries, sorted by internal
// key, terminated by a checksum over the entries region.
//
//	entry := keyLen (varint) | internalKey | valLen (varint) | value
//	block := entry* | crc (4)
package sstable

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

const (
	blockTrailerSize = 4
	targetBlockSize  = 4 << 10
)

type blockBuilder struct {
	buf     []byte
	lastKey []byte
}

// newBlockBuilder returns an empty builder ready for Add.
func newBlockBuilder() *blockBuilder {
	return &blockBuilder{
		buf:     make([]byte, 0, targetBlockSize),
		lastKey: nil,
	}
}

func (b *blockBuilder) Add(key, val []byte) {
	contract.Require(len(key) >= keys.TrailerSize,
		"blockBuilder.Add: key %x is %d bytes, an internal key needs at least %d",
		key, len(key), keys.TrailerSize)
	contract.Require(b.Empty() || keys.Compare(key, b.lastKey) > 0,
		"blockBuilder.Add: key %x is not greater than lastKey %x", key, b.lastKey)

	b.buf = binary.AppendUvarint(b.buf, uint64(len(key)))
	b.buf = append(b.buf, key...)
	b.buf = binary.AppendUvarint(b.buf, uint64(len(val)))
	b.buf = append(b.buf, val...)

	b.lastKey = append(b.lastKey[:0], key...)
}

func (b *blockBuilder) Full() bool {
	return len(b.buf) >= targetBlockSize
}

func (b *blockBuilder) Empty() bool {
	return len(b.buf) == 0
}

func (b *blockBuilder) Finish() []byte {
	crc := crc32.Checksum(b.buf, castagnoli)
	b.buf = binary.LittleEndian.AppendUint32(b.buf, crc)
	return b.buf
}

func (b *blockBuilder) Reset() {
	b.buf = b.buf[:0]
	b.lastKey = b.lastKey[:0]
}
