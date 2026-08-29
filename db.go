package tinylsm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/memtable"
	"github.com/ogzhanolguncu/tinylsm/wal"
)

const skiplistSeed = 1

type Options struct {
	MemtableThreshold uint64
}

type DB struct {
	mu      sync.RWMutex
	dir     string
	mem     *memtable.Memtable
	nextSeq uint64
	opts    Options
}

func Open(dir string, opts Options) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	if err != nil {
		return nil, err
	}

	var mem *memtable.Memtable
	var nextSeq uint64

	if len(matches) == 0 {
		mt, err := memtable.New(
			filepath.Join(dir, wal.FileName(1)),
			skiplistSeed,
		)
		if err != nil {
			return nil, err
		}
		mem = mt
		nextSeq = 0
	}
	if len(matches) == 1 {
		mt, seq, err := memtable.Open(matches[0], skiplistSeed)
		if err != nil {
			return nil, err
		}
		nextSeq = seq
		mem = mt
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("matches cannot be more than 1, but it's %d", len(matches))
	}

	return &DB{
		mem:     mem,
		dir:     dir,
		mu:      sync.RWMutex{},
		nextSeq: nextSeq,
		opts:    opts,
	}, nil
}

func (db *DB) Put(key, val []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.mem.Put(key, val, db.nextSeq); err != nil {
		return err
	}
	db.nextSeq++
	return nil
}

func (db *DB) Delete(key []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.mem.Delete(key, db.nextSeq); err != nil {
		return err
	}
	db.nextSeq++
	return nil
}

func (db *DB) Get(key []byte) ([]byte, bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	val, state := db.mem.Get(key)

	switch state {
	case keys.Found:
		return val, true, nil
	case keys.NotFound, keys.Deleted:
		return nil, false, nil
	default:
		return nil, false, nil
	}
}

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.mem.Close()
}
