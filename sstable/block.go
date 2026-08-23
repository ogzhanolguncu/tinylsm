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
	"errors"
	"fmt"
	"hash/crc32"
	"sort"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var (
	ErrBlockChecksum = errors.New("sstable: block checksum mismatch")
	ErrBlockCorrupt  = errors.New("sstable: corrupt block")
)

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

	contract.Ensure(len(b.buf) > blockTrailerSize,
		"blockBuilder.Finish: %d bytes is a bare trailer, newBlock rejects it", len(b.buf))
	return b.buf
}

func (b *blockBuilder) Reset() {
	b.buf = b.buf[:0]
	b.lastKey = b.lastKey[:0]
}

type block struct {
	data    []byte
	offsets []int
}

func newBlock(data []byte) (*block, error) {
	if len(data) <= blockTrailerSize {
		return nil, fmt.Errorf("%w: too short for trailer", ErrBlockCorrupt)
	}
	crc := binary.LittleEndian.Uint32(data[len(data)-4:])
	data = data[:len(data)-4]
	if crc32.Checksum(data, castagnoli) != crc {
		return nil, ErrBlockChecksum
	}

	block := &block{data: data}

	off := 0
	for off < len(data) {
		block.offsets = append(block.offsets, off)
		keyLen, n := binary.Uvarint(data[off:])
		if n <= 0 {
			return nil, fmt.Errorf("%w: bad keyLen varint", ErrBlockCorrupt)
		}
		if keyLen < keys.TrailerSize {
			return nil, fmt.Errorf("%w: key shorter than trailer", ErrBlockCorrupt)
		}
		off += n
		if rem := uint64(len(data) - off); keyLen > rem {
			return nil, fmt.Errorf("%w: keyLen overruns block", ErrBlockCorrupt)
		}
		off += int(keyLen)

		valLen, n := binary.Uvarint(data[off:])
		if n <= 0 {
			return nil, fmt.Errorf("%w: bad valLen varint", ErrBlockCorrupt)
		}
		off += n
		if rem := uint64(len(data) - off); valLen > rem {
			return nil, fmt.Errorf("%w: valLen overruns block", ErrBlockCorrupt)
		}
		off += int(valLen)
	}

	contract.Ensure(off == len(data), "newBlock: entries end at %d, block is %d bytes", off, len(data))

	return block, nil
}

type blockIter struct {
	b *block
	i int
}

func (b *blockIter) Valid() bool {
	return b.i < len(b.b.offsets)
}

func (b *blockIter) Key() []byte {
	contract.Require(b.Valid(), "blockIter.Key: iterator is exhausted")
	return b.keyAt(b.i)
}

func (b *blockIter) Value() []byte {
	contract.Require(b.Valid(), "blockIter.Value: iterator is exhausted")
	off := b.b.offsets[b.i]

	keyLen, n := binary.Uvarint(b.b.data[off:])
	off += n + int(keyLen)

	valLen, n := binary.Uvarint(b.b.data[off:])
	off += n
	value := b.b.data[off : off+int(valLen)]

	return value
}

func (b *blockIter) Next() {
	contract.Require(b.Valid(), "blockIter.Next: iterator is exhausted")
	b.i++
}

func (b *blockIter) keyAt(i int) []byte {
	off := b.b.offsets[i]

	keyLen, n := binary.Uvarint(b.b.data[off:])
	off += n

	key := b.b.data[off : off+int(keyLen)]
	return key
}

func (b *blockIter) Seek(target []byte) {
	b.i = sort.Search(len(b.b.offsets), func(i int) bool {
		return keys.Compare(b.keyAt(i), target) >= 0
	})
}
