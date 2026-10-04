// Package sstable is a finished .sst file: data blocks, then a bloom filter over
// the table's user keys, then an index block whose entries map each data
// block's last internal key to that block's location, then a fixed-width footer.
//
//	table  := dataBlock* | filter | indexBlock | footer
//	filter := bloom bits | k (1) | crc (4)
//	footer := indexHandle (16) | filterHandle (16) | maxSeq (8) | magic (8)
//	handle := off (8) | size (8), little-endian
//
// Format v3 added the filter. Older tables are rejected by the magic check:
// tinylsm has no data worth migrating, so there is no v2 reader.
package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/ogzhanolguncu/tinylsm/bloom"
	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

const (
	footerSize uint64 = 48
	// blockHandleSize is an off|size pair: two little-endian uint64s.
	blockHandleSize = 16
	// tableMagic identifies a tinylsm SSTable, format v3 ("OzLSM\x00\x00\x03").
	tableMagic uint64 = 0x0300004D534C7A4F
)

var ErrBadMagic = errors.New("sstable: bad magic")

type indexEntry struct {
	lastKey []byte
	off     uint64
	size    uint64
}

type Writer struct {
	idx    []indexEntry
	bb     *blockBuilder
	f      *os.File
	off    uint64
	maxSeq uint64
	// userKeys feed the bloom filter: each user key once, however many
	// versions of it the table holds.
	userKeys [][]byte
}

func NewWriter(path string) (*Writer, error) {
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
	return &Writer{
		f: f, bb: newBlockBuilder(),
	}, nil
}

func (tw *Writer) Add(key, val []byte) error {
	uk, maxSeq, _, err := keys.Decode(key)
	if err != nil {
		return err
	}
	tw.maxSeq = max(maxSeq, tw.maxSeq)
	// keys arrive sorted, so all versions of a user key are adjacent
	if n := len(tw.userKeys); n == 0 || !bytes.Equal(tw.userKeys[n-1], uk) {
		tw.userKeys = append(tw.userKeys, bytes.Clone(uk))
	}
	tw.bb.Add(key, val)
	if tw.bb.Full() {
		return tw.flushBlock()
	}
	return nil
}

func (tw *Writer) Finish() error {
	err := tw.flushBlock()
	if err != nil {
		return err
	}

	filter := bloom.Build(tw.userKeys, bloom.BitsPerKey)
	filter = binary.LittleEndian.AppendUint32(filter, crc32.Checksum(filter, castagnoli))
	if _, err := tw.f.Write(filter); err != nil {
		return err
	}
	filterOff, filterSize := tw.off, uint64(len(filter))
	indexOff := filterOff + filterSize

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
	binary.LittleEndian.PutUint64(footer[16:24], filterOff)
	binary.LittleEndian.PutUint64(footer[24:32], filterSize)
	binary.LittleEndian.PutUint64(footer[32:40], tw.maxSeq)
	binary.LittleEndian.PutUint64(footer[40:48], tableMagic)
	if _, err := tw.f.Write(footer); err != nil {
		return err
	}
	if err := tw.f.Sync(); err != nil {
		return err
	}
	return tw.f.Close()
}

func (tw *Writer) flushBlock() error {
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

	if n := len(tw.idx); n == 1 {
		contract.Ensure(idxEntry.off == 0, "tableWriter: first data block starts at %d, not 0", idxEntry.off)
	} else {
		prev := tw.idx[n-2]
		contract.Ensure(idxEntry.off == prev.off+prev.size,
			"tableWriter: block %d starts at %d, previous block ends at %d", n-1, idxEntry.off, prev.off+prev.size)
		contract.Ensure(keys.Compare(idxEntry.lastKey, prev.lastKey) > 0,
			"tableWriter: block %d last key %x does not sort after %x", n-1, idxEntry.lastKey, prev.lastKey)
	}

	tw.off += uint64(len(data))

	tw.bb.Reset()
	return nil
}

type Table struct {
	f      *os.File
	fsize  uint64
	index  *block
	filter []byte // bloom bits | k, CRC already checked and stripped
	maxSeq uint64
	refs   atomic.Int32
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

func Open(path string) (_ *Table, err error) {
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

	magic := binary.LittleEndian.Uint64(footer[40:footerSize])
	if magic != tableMagic {
		return nil, ErrBadMagic
	}

	maxSeq := binary.LittleEndian.Uint64(footer[32:40])
	if maxSeq > keys.MaxSeq {
		return nil, ErrBlockCorrupt
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

	// The filter sits right before the index. A filter that lies would make
	// Get skip a table holding the key, so it is checksummed and verified here.
	fOff, fSize, err := blockHandle(footer[16:32])
	if err != nil {
		return nil, err
	}
	if fSize < blockTrailerSize+2 || fSize > off || fOff != off-fSize {
		return nil, ErrBlockCorrupt
	}
	filter := make([]byte, fSize)
	if _, err := f.ReadAt(filter, int64(fOff)); err != nil {
		return nil, fmt.Errorf("failed to read filter: %w", err)
	}
	body := filter[:fSize-blockTrailerSize]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(filter[fSize-blockTrailerSize:]) {
		return nil, ErrBlockChecksum
	}

	t := &Table{
		index:  idx,
		filter: body,
		f:      f,
		fsize:  uint64(fsize),
		maxSeq: maxSeq,
	}
	t.refs.Store(1)
	return t, nil
}

func (t *Table) Get(target []byte) ([]byte, keys.LookupState, error) {
	uk, _, _, err := keys.Decode(target)
	if err != nil {
		return nil, keys.NotFound, err
	}
	// The cheap check first: most tables don't hold most keys.
	if !bloom.MayContain(t.filter, uk) {
		return nil, keys.NotFound, nil
	}

	it := &blockIter{b: t.index, i: 0}
	it.Seek(target)
	if it.Valid() {
		val := it.Value()
		off, size, err := blockHandle(val)
		if err != nil {
			return nil, keys.NotFound, err
		}

		blk, err := readBlock(t.f, off, size, t.fsize)
		if err != nil {
			return nil, keys.NotFound, err
		}
		dit := &blockIter{b: blk}
		dit.Seek(target)
		if dit.Valid() {
			ditUserKey, _, ditKind, err := keys.Decode(dit.Key())
			if err != nil {
				return nil, keys.NotFound, err
			}

			targetUserKey, _, _, err := keys.Decode(target)
			if err != nil {
				return nil, keys.NotFound, err
			}

			if !bytes.Equal(ditUserKey, targetUserKey) {
				return nil, keys.NotFound, nil
			}

			switch ditKind {
			case keys.KindPut:
				return dit.Value(), keys.Found, nil
			case keys.KindDelete:
				return nil, keys.Deleted, nil
			default:
				return nil, keys.NotFound, fmt.Errorf("unknown kind %d in data block at %d: %w", ditKind, off, ErrBlockCorrupt)
			}

		}

	}
	return nil, keys.NotFound, nil
}

func (t *Table) MaxSeq() uint64 { return t.maxSeq }

func (t *Table) NewIterator() *Iter {
	return &Iter{
		indexIt: &blockIter{b: t.index, i: 0},
		dataIt:  nil,
		t:       t,
		block:   nil,
		err:     nil,
	}
}

func (t *Table) Ref() {
	contract.Require(t.refs.Add(1) > 1, "Table.Ref: table already closed")
}

func (t *Table) Unref() error {
	n := t.refs.Add(-1)
	if n < 0 {
		t.refs.Add(1)
		return os.ErrClosed
	}
	if n == 0 {
		return t.f.Close()
	}
	return nil
}

// Close drops the reference Open handed out.
func (t *Table) Close() error { return t.Unref() }

func FileName(num uint64) string {
	return fmt.Sprintf("%09d.sst", num)
}
