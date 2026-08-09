package memtable

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
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
	closed     atomic.Bool
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

func Open(path string, seed int64) (*Memtable, error) {
	entries, err := wal.Replay(path)
	if err != nil {
		return nil, fmt.Errorf("memtable: open fail: %w", err)
	}

	writer, err := wal.OpenWriter(path)
	if err != nil {
		return nil, err
	}
	skiplist := sl.New(keys.Compare, seed)

	var approxSize, nextSeq uint64

	if len(entries) > 0 {
		for _, e := range entries {
			if e.Seq > nextSeq {
				nextSeq = e.Seq
			}
		}
		nextSeq++
	}

	for _, e := range entries {
		ik, err := keys.Encode(e.Key, e.Seq, keys.Kind(e.Kind))
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("memtable: encode fail: %w", err)
		}

		skiplist.Insert(ik, e.Value)
		approxSize += uint64(len(ik)) + uint64(len(e.Value))
	}

	return &Memtable{
		skiplist:   skiplist,
		writer:     writer,
		nextSeq:    nextSeq,
		approxSize: approxSize,
	}, nil
}

func (mt *Memtable) Put(key, val []byte) error {
	return mt.mutate(key, val, wal.KindPut)
}

func (mt *Memtable) Delete(key []byte) error {
	return mt.mutate(key, nil, wal.KindDelete)
}

func (mt *Memtable) Get(key []byte) ([]byte, bool) {
	if mt.closed.Load() {
		// its closed
		return nil, false
	}

	ik, err := keys.Encode(key, keys.MaxSeq, keys.KindPut)
	if err != nil {
		return nil, false
	}

	it := mt.skiplist.NewIterator()
	it.Seek(ik)
	if !it.Valid() {
		return nil, false
	}

	encodedKey := it.Key()
	skVal := it.Value()

	// Only a bug in mutate can put a key shorter than a trailer into the skiplist.
	userKey, _, kind, err := keys.Decode(encodedKey)
	contract.Invariant(err == nil, "memtable.Get: skiplist holds a malformed internal key %q: %v", encodedKey, err)

	// Overshoot before kind: the kind of an entry belonging to another user key
	// says nothing about this one.
	if !bytes.Equal(userKey, key) {
		return nil, false
	}
	if kind == keys.KindDelete {
		return nil, false
	}
	return skVal, true
}

func (mt *Memtable) mutate(key, val []byte, kind wal.Kind) error {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if mt.closed.Load() {
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
	if mt.closed.Load() {
		return ErrClosed
	}
	mt.closed.Store(true)
	return mt.writer.Close()
}
