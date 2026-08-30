package tinylsm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/memtable"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
	"github.com/ogzhanolguncu/tinylsm/sstable"
	"github.com/ogzhanolguncu/tinylsm/wal"
)

// maxSize when we hit that threshold we'll freeze this and write it out to a SSTable
const (
	memBytesMax  = 1024 * 1024 * 4
	skiplistSeed = 1
)

type Options struct {
	MemtableThreshold uint64
}

type DB struct {
	mu          sync.RWMutex
	dir         string
	mem         *memtable.Memtable
	imm         *memtable.Memtable
	nextSeq     uint64
	nextFileNum uint64
	l0          []*sstable.Table
	opts        Options
}

func Open(dir string, opts Options) (*DB, error) {
	if opts.MemtableThreshold == 0 {
		opts.MemtableThreshold = memBytesMax
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	sstMatches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	if err != nil {
		return nil, err
	}

	walMatches, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	if err != nil {
		return nil, err
	}

	max := uint64(0)
	for _, m := range sstMatches {
		n, err := strconv.ParseUint(filepath.Base(m)[:9], 10, 64)
		if err != nil {
			return nil, err
		}
		if n > max {
			max = n
		}
	}

	for _, m := range walMatches {
		n, err := strconv.ParseUint(filepath.Base(m)[:9], 10, 64)
		if err != nil {
			return nil, err
		}
		if n > max {
			max = n
		}
	}

	var mem *memtable.Memtable
	var nextSeq uint64

	// One counter, bumped everywhere a file number is taken — including here,
	// or the first freeze() hands out this WAL's number a second time.
	nextFileNum := max + 1

	if len(walMatches) == 0 {
		num := nextFileNum
		nextFileNum++
		mt, err := memtable.New(
			filepath.Join(dir, wal.FileName(num)),
			skiplistSeed,
		)
		if err != nil {
			return nil, err
		}
		mem = mt
		nextSeq = 0
	}
	if len(walMatches) == 1 {
		mt, seq, err := memtable.Open(walMatches[0], skiplistSeed)
		if err != nil {
			return nil, err
		}
		nextSeq = seq
		mem = mt
	}
	if len(walMatches) > 1 {
		return nil, fmt.Errorf("matches cannot be more than 1, but it's %d", len(walMatches))
	}

	var l0 []*sstable.Table
	// if we find a broken sstable we have to close each of them coz we return nil, and
	// caller has no way to close them back
	for _, m := range sstMatches {
		t, err := sstable.Open(m)
		if err != nil {
			for _, opened := range l0 {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("open sstable %s: %w", m, err)
		}
		l0 = append(l0, t)
	}

	return &DB{
		l0:          l0,
		mem:         mem,
		dir:         dir,
		mu:          sync.RWMutex{},
		nextSeq:     nextSeq,
		opts:        opts,
		nextFileNum: nextFileNum,
	}, nil
}

func (db *DB) Put(key, val []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.makeRoomForWriter(); err != nil {
		return err
	}

	if err := db.mem.Put(key, val, db.nextSeq); err != nil {
		return err
	}
	db.nextSeq++
	return nil
}

func (db *DB) Delete(key []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.makeRoomForWriter(); err != nil {
		return err
	}

	if err := db.mem.Delete(key, db.nextSeq); err != nil {
		return err
	}
	db.nextSeq++
	return nil
}

func (db *DB) makeRoomForWriter() error {
	if db.mem.ApproxSize() >= db.opts.MemtableThreshold {
		if err := db.freeze(); err != nil {
			return err
		}
		return db.flush()
	}
	return nil
}

func (db *DB) Get(key []byte) ([]byte, bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	ik, err := keys.Encode(key, keys.MaxSeq, keys.KindPut)
	if err != nil {
		return nil, false, err
	}

	if val, state := db.mem.Get(key); state != keys.NotFound {
		return val, state == keys.Found, nil
	}
	if db.imm != nil {
		if val, state := db.imm.Get(key); state != keys.NotFound {
			return val, state == keys.Found, nil
		}
	}

	for i := len(db.l0) - 1; i >= 0; i-- {
		val, state, err := db.l0[i].Get(ik)
		if err != nil {
			return nil, false, err
		}
		if state != keys.NotFound {
			return val, state == keys.Found, nil
		}
	}
	return nil, false, nil
}

func (db *DB) flush() error {
	contract.Require(db.imm != nil, "flush: nothing frozen")

	fileNum := db.nextFileNum
	db.nextFileNum++

	path := filepath.Join(db.dir, sstable.FileName(fileNum))
	w, err := sstable.NewWriter(path)
	if err != nil {
		return err
	}
	it := db.imm.NewIterator()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if err := w.Add(it.Key(), it.Value()); err != nil {
			return err
		}
	}
	if err := w.Finish(); err != nil {
		return err
	}

	sst, err := sstable.Open(path)
	if err != nil {
		return err
	}
	db.l0 = append(db.l0, sst)

	_ = db.imm.Close()
	_ = os.Remove(db.imm.Path())
	db.imm = nil
	return nil
}

func (db *DB) freeze() error {
	fileNum := db.nextFileNum
	db.nextFileNum++

	mem, err := memtable.New(filepath.Join(db.dir, wal.FileName(fileNum)), skiplistSeed)
	if err != nil {
		return err
	}
	db.imm = db.mem
	db.mem = mem
	return nil
}

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.mem.Close()
}
