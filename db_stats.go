package tinylsm

import (
	"os"
	"path/filepath"

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
