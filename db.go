package tinylsm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/manifest"
	"github.com/ogzhanolguncu/tinylsm/memtable"
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
	"github.com/ogzhanolguncu/tinylsm/sstable"
	"github.com/ogzhanolguncu/tinylsm/wal"
)

var ErrUnknownFileName = errors.New("tinylsm: unknown file name")

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
	manifest    *manifest.Writer
	version     *manifest.Version
}

func Open(dir string, opts Options) (*DB, error) {
	if opts.MemtableThreshold == 0 {
		opts.MemtableThreshold = memBytesMax
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	var version *manifest.Version
	name, err := manifest.ReadCurrent(dir)

	if errors.Is(err, fs.ErrNotExist) {
		version = &manifest.Version{}
	} else if err != nil {
		return nil, err
	} else {
		version, err = manifest.Fold(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}

	}

	sstMatches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	if err != nil {
		return nil, err
	}
	for _, m := range sstMatches {
		n, err := parseFileNum(m)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(version.Files[0], func(fm manifest.FileMeta) bool {
			return fm.FileNum == n
		}) {
			_ = os.Remove(m)
		}
	}

	walMatches, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	if err != nil {
		return nil, err
	}

	maxFileNum := uint64(0)

	for _, m := range walMatches {
		n, err := parseFileNum(m)
		if err != nil {
			return nil, err
		}
		if n > maxFileNum {
			maxFileNum = n
		}
	}

	var mem *memtable.Memtable
	var nextSeq uint64

	// One counter, bumped everywhere a file number is taken — including here,
	// or the first freeze() hands out this WAL's number a second time.
	nextFileNum := max(maxFileNum+1, version.NextFileNum)

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
	// More than one WAL means a crash between freeze and flush. The newest is
	// the live memtable; older ones are flushed once the DB is assembled.
	// Glob sorts lexically, which is numeric order for zero-padded names.
	var stale []*memtable.Memtable
	for i, m := range walMatches {
		mt, seq, err := memtable.Open(m, skiplistSeed)
		if err != nil {
			for _, s := range stale {
				_ = s.Close()
			}
			return nil, err
		}
		nextSeq = max(nextSeq, seq)
		if i == len(walMatches)-1 {
			mem = mt
		} else {
			stale = append(stale, mt)
		}
	}

	var l0 []*sstable.Table
	// if we find a broken sstable we have to close each of them coz we return nil, and
	// caller has no way to close them back
	files := version.Files[0]
	for _, fm := range files {
		t, err := sstable.Open(filepath.Join(dir, sstable.FileName(fm.FileNum)))
		if err != nil {
			for _, opened := range l0 {
				_ = opened.Close()
			}
			_ = mem.Close()
			return nil, fmt.Errorf("open sstable %d: %w", fm.FileNum, err)
		}
		nextSeq = max(nextSeq, t.MaxSeq()+1)
		l0 = append(l0, t)
	}

	num := nextFileNum
	nextFileNum++
	newName := fmt.Sprintf("MANIFEST-%06d", num)
	mw, err := manifest.Create(filepath.Join(dir, newName))
	if err != nil {
		return nil, err
	}

	lastSeq := nextSeq
	if nextSeq > 0 {
		lastSeq -= 1
	}
	err = mw.Append(manifest.VersionEdit{
		AddFiles:    version.Files[0],
		NextFileNum: nextFileNum,
		LastSeq:     lastSeq,
	})
	if err != nil {
		return nil, err
	}
	err = manifest.WriteCurrent(dir, newName)
	if err != nil {
		return nil, err
	}
	if name != "" {
		_ = os.Remove(filepath.Join(dir, name))
	}

	db := &DB{
		l0:          l0,
		mem:         mem,
		dir:         dir,
		mu:          sync.RWMutex{},
		nextSeq:     nextSeq,
		opts:        opts,
		nextFileNum: nextFileNum,
		version:     version,
		manifest:    mw,
	}

	for i, mt := range stale {
		if mt.ApproxSize() == 0 {
			_ = mt.Close()
			_ = os.Remove(mt.Path())
			continue
		}
		db.imm = mt
		if err := db.flush(); err != nil {
			for _, s := range stale[i+1:] {
				_ = s.Close()
			}
			_ = db.Close()
			return nil, fmt.Errorf("flush stale wal: %w", err)
		}
	}
	return db, nil
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

	versionEdit := manifest.VersionEdit{
		AddFiles: []manifest.FileMeta{
			{Level: 0, FileNum: fileNum},
		},
		NextFileNum: db.nextFileNum,
		LastSeq:     db.nextSeq - 1,
	}
	err = db.manifest.Append(versionEdit)
	if err != nil {
		return fmt.Errorf("failed to append to manifest: %w", err)
	}
	db.version = db.version.Apply(versionEdit)
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
	errs := []error{}
	if db.imm != nil {
		errs = append(errs, db.imm.Close())
	}
	for _, t := range db.l0 {
		errs = append(errs, t.Close())
	}
	errs = append(errs, db.mem.Close(), db.manifest.Close())
	return errors.Join(errs...)
}

func parseFileNum(base string) (uint64, error) {
	b := filepath.Base(base)
	// Its 9 digits + 1 dot + 3 file extension sst | wal
	if len(b) != 13 {
		return 0, ErrUnknownFileName
	}
	return strconv.ParseUint(filepath.Base(b)[:9], 10, 64)
}
