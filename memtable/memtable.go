package memtable

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ogzhanolguncu/tinylsm/keys"
	sl "github.com/ogzhanolguncu/tinylsm/skiplist"
	"github.com/ogzhanolguncu/tinylsm/wal"
)

var ErrClosed = errors.New("memtable: closed")

// maxSize when we hit that threshold we'll freeze this and write it out to a SSTable
const maxSize = 1024 * 1024 * 4

type Memtable struct {
	skiplist   *sl.SkipList
	writer     *wal.Writer
	nextSeq    uint64
	mu         sync.Mutex
	approxSize uint64
	closed     bool
}

func New(path string, seed int64) (*Memtable, error) {
	skiplist := sl.New(keys.Compare, seed)
	writer, err := wal.NewWriter(path)
	if err != nil {
		return nil, err
	}

	return &Memtable{
		skiplist:   skiplist,
		writer:     writer,
		nextSeq:    0,
		approxSize: 0,
	}, nil
}

func (mt *Memtable) Put(key, val []byte) error {
	return mt.mutate(key, val, wal.KindPut)
}

func (mt *Memtable) Delete(key []byte) error {
	return mt.mutate(key, nil, wal.KindDelete)
}

func (mt *Memtable) mutate(key, val []byte, kind wal.Kind) error {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if mt.closed {
		return ErrClosed
	}

	seq := mt.nextSeq

	ik, err := keys.Encode(key, seq, keys.Kind(kind))
	if err != nil {
		return fmt.Errorf("memtable: encode fail: %w", err)
	}

	err = mt.writer.Append(wal.Entry{
		Key:   key,
		Value: val,
		Seq:   seq,
		Kind:  kind,
	})
	if err != nil {
		return fmt.Errorf("memtable: append fail: %w", err)
	}

	mt.nextSeq++
	mt.approxSize += uint64(len(ik)) + uint64(len(val))

	mt.skiplist.Insert(ik, val)
	return nil
}

func (mt *Memtable) Close() error {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	if mt.closed {
		return ErrClosed
	}
	mt.closed = true
	return mt.writer.Close()
}
