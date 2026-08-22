// Package sstable implements the on-disk sorted string table format.
//
// A block is the unit of I/O: a contiguous run of entries, sorted by internal
// key, terminated by a checksum over the entries region.
//
//	entry := keyLen (varint) | internalKey | valLen (varint) | value
//	block := entry* | crc (4)
package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

const (
	footerSize uint64 = 24
	// blockHandleSize is an off|size pair: two little-endian uint64s.
	blockHandleSize = 16
	// tableMagic identifies a tinylsm SSTable.
	tableMagic uint64 = 0x0100004D534C7A4F
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var (
	ErrBlockChecksum = errors.New("sstable: block checksum mismatch")
	ErrBlockCorrupt  = errors.New("sstable: corrupt block")
	ErrBadMagic      = errors.New("sstable: bad magic")
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
	binary.LittleEndian.PutUint64(footer[16:24], tableMagic)
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

type Table struct {
	f     *os.File
	fsize uint64
	index *block
}

// 16-byte off|size pair, LevelDB calls this a BlockHandle.
func blockHandle(v []byte) (off, size uint64, err error) {
	if len(v) != blockHandleSize {
		return 0, 0, fmt.Errorf("block handle is %d bytes, want %d: %w", len(v), blockHandleSize, ErrBlockCorrupt)
	}
	off = binary.LittleEndian.Uint64(v[0:8])
	size = binary.LittleEndian.Uint64(v[8:16])
	return off, size, nil
}

// reads one block at off/size, bounds-checked against fileSize before allocating.
func readBlock(f *os.File, off, size, fsize uint64) (*block, error) {
	if off > fsize || size > fsize-off {
		return nil, ErrBlockCorrupt
	}

	buf := make([]byte, size)
	_, err := f.ReadAt(buf, int64(off))
	if err != nil {
		return nil, fmt.Errorf("failed to read block: %w", err)
	}
	b, err := newBlock(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to build new block: %w", err)
	}
	return b, nil
}

func openTable(path string) (_ *Table, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	fstat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	fsize := fstat.Size()

	if fsize < int64(footerSize) {
		return nil, ErrBlockCorrupt
	}

	footer := make([]byte, footerSize)
	_, err = f.ReadAt(footer, fsize-int64(footerSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read footer %w", err)
	}

	magic := binary.LittleEndian.Uint64(footer[blockHandleSize:footerSize])
	if magic != tableMagic {
		return nil, ErrBadMagic
	}
	off, size, err := blockHandle(footer[:blockHandleSize])
	if err != nil {
		return nil, err
	}
	// index block must end exactly where the footer begins
	if size > uint64(fsize)-footerSize || off != uint64(fsize)-footerSize-size {
		return nil, ErrBlockCorrupt
	}
	idx, err := readBlock(f, off, size, uint64(fsize))
	if err != nil {
		return nil, err
	}

	return &Table{
		index: idx,
		f:     f,
		fsize: uint64(fsize),
	}, nil
}

type LookupState uint8

const (
	Found LookupState = iota
	Deleted
	NotFound
)

func (t *Table) Get(target []byte) ([]byte, LookupState, error) {
	it := &blockIter{b: t.index, i: 0}
	it.Seek(target)
	if it.Valid() {
		val := it.Value()
		off, size, err := blockHandle(val)
		if err != nil {
			return nil, NotFound, err
		}

		blk, err := readBlock(t.f, off, size, t.fsize)
		if err != nil {
			return nil, NotFound, err
		}
		dit := &blockIter{b: blk}
		dit.Seek(target)
		if dit.Valid() {
			ditUserKey, _, ditKind, err := keys.Decode(dit.Key())
			if err != nil {
				return nil, NotFound, err
			}

			targetUserKey, _, _, err := keys.Decode(target)
			if err != nil {
				return nil, NotFound, err
			}

			if !bytes.Equal(ditUserKey, targetUserKey) {
				return nil, NotFound, nil
			}

			switch ditKind {
			case keys.KindPut:
				return dit.Value(), Found, nil
			case keys.KindDelete:
				return nil, Deleted, nil
			default:
				return nil, NotFound, fmt.Errorf("unknown kind %d in data block at %d: %w", ditKind, off, ErrBlockCorrupt)
			}

		}

	}
	return nil, NotFound, nil
}

func (t *Table) Close() error {
	return t.f.Close()
}

func FileName(num uint64) string {
	return fmt.Sprintf("%09d.sst", num)
}
