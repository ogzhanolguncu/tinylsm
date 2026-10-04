package tinylsm

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/ogzhanolguncu/tinylsm/merge"
	"github.com/ogzhanolguncu/tinylsm/sstable"
)

type TableStats struct {
	FileNum uint64
	Bytes   int64
}

type Stats struct {
	MemBytes     uint64
	MemThreshold uint64
	L0           []TableStats
	NextSeq      uint64
	NextFileNum  uint64
}

// Stats is a read-only snapshot for tooling (the REPL); it plays no part in the engine.
func (db *DB) Stats() Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()

	s := Stats{
		MemBytes:     db.mem.ApproxSize(),
		MemThreshold: db.opts.MemtableThreshold,
		NextSeq:      db.nextSeq,
		NextFileNum:  db.nextFileNum,
	}
	for _, f := range db.version.Files[0] {
		ts := TableStats{FileNum: f.FileNum}
		if info, err := os.Stat(filepath.Join(db.dir, sstable.FileName(f.FileNum))); err == nil {
			ts.Bytes = info.Size()
		}
		s.L0 = append(s.L0, ts)
	}
	return s
}

// RawScan walks every entry the DB holds, in internal-key order: all versions,
// tombstones included. Tooling only: it holds the read lock for the whole walk,
// which a real Scan must not do.
func (db *DB) RawScan(fn func(internalKey, val []byte) bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	m := merge.New(db.children())
	for m.SeekToFirst(); m.Valid(); m.Next() {
		if !fn(m.Key(), m.Value()) {
			return
		}
	}
}

// Scan walks live user keys in [from, to) in order, newest value each.
// Empty from starts at the beginning; empty to runs to the end.
//
// The lock is held only while grabbing the iterators, so writers never wait
// on a scan. Writes that land mid-scan are hidden by the snapshot seq.
// Not yet safe against tables being deleted mid-scan: Phase 8 problem.
func (db *DB) Scan(from, to []byte, fn func(key, val []byte) bool) error {
	db.mu.RLock()
	children, snapshot := db.children(), db.nextSeq
	db.mu.RUnlock()

	d := merge.NewDBIter(merge.New(children), snapshot)
	for d.Seek(from); d.Valid(); d.Next() {
		if len(to) > 0 && bytes.Compare(d.Key(), to) >= 0 {
			break
		}
		if !fn(d.Key(), d.Value()) {
			break
		}
	}
	return d.Error()
}

func (db *DB) children() []merge.Iterator {
	children := []merge.Iterator{db.mem.NewIterator()}
	if db.imm != nil {
		children = append(children, db.imm.NewIterator())
	}
	for _, t := range db.l0 {
		children = append(children, t.NewIterator())
	}
	return children
}
