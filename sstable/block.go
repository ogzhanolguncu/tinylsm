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
	"os"
	"path/filepath"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

const (
	footerSize uint64 = 24
	// magic identifies a tinylsm SSTable.
	magic uint64 = 0x0100004D534C7A4F
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
	for off != len(data) {
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
	off := b.b.offsets[b.i]

	keyLen, n := binary.Uvarint(b.b.data[off:])
	off += n

	key := b.b.data[off : off+int(keyLen)]
	return key
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

type indexEntry struct {
	lastKey []byte
	off     uint64
	size    uint64
}

type tableWriter struct {
	idx []indexEntry
	bb  *blockBuilder
	f   *os.File
	off uint64
}

func newTableWriter(path string) (*tableWriter, error) {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open sst dir: %w", err)
	}
	defer func() {
		_ = d.Close()
	}()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create sst file: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sync sst dir: %w", err)
	}
	return &tableWriter{
		f: f, bb: newBlockBuilder(),
	}, nil
}

func (tw *tableWriter) Add(key, val []byte) error {
	tw.bb.Add(key, val)
	if tw.bb.Full() {
		return tw.flushBlock()
	}
	return nil
}

func (tw *tableWriter) Finish() error {
	err := tw.flushBlock()
	if err != nil {
		return err
	}

	indexOff := tw.off

	idxBlock := newBlockBuilder()
	for _, e := range tw.idx {
		val := make([]byte, 16)
		binary.LittleEndian.PutUint64(val[0:8], e.off)
		binary.LittleEndian.PutUint64(val[8:16], e.size)
		idxBlock.Add(e.lastKey, val)
	}
	data := idxBlock.Finish()
	if _, err := tw.f.Write(data); err != nil {
		return err
	}
	indexSize := uint64(len(data))

	footer := make([]byte, footerSize)
	binary.LittleEndian.PutUint64(footer[0:8], indexOff)
	binary.LittleEndian.PutUint64(footer[8:16], indexSize)
	binary.LittleEndian.PutUint64(footer[16:24], magic)
	if _, err := tw.f.Write(footer); err != nil {
		return err
	}
	if err := tw.f.Sync(); err != nil {
		return err
	}
	return tw.f.Close()
}

func (tw *tableWriter) flushBlock() error {
	if tw.bb.Empty() {
		return nil
	}
	data := tw.bb.Finish()
	if _, err := tw.f.Write(data); err != nil {
		return err
	}
	idxEntry := indexEntry{
		lastKey: append([]byte(nil), tw.bb.lastKey...),
		off:     tw.off,
		size:    uint64(len(data)),
	}
	tw.idx = append(tw.idx, idxEntry)
	tw.off += uint64(len(data))

	tw.bb.Reset()
	return nil
}

func FileName(num uint64) string {
	return fmt.Sprintf("%09d.sst", num)
}
