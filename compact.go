package tinylsm

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/manifest"
	"github.com/ogzhanolguncu/tinylsm/merge"
	"github.com/ogzhanolguncu/tinylsm/sstable"
)

// Compact merges every L0 table into one, keeping only the newest live
// version of each key.
func (db *DB) Compact() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.compact()
}

func (db *DB) compact() error {
	if len(db.l0) == 0 {
		return nil
	}

	list := make([]merge.Iterator, len(db.l0))
	for i, t := range db.l0 {
		list[i] = t.NewIterator()
	}
	d := merge.NewDBIter(merge.New(list), keys.MaxSeq)

	var (
		w       *sstable.Writer
		fileNum uint64
		path    string
	)
	for d.SeekToFirst(); d.Valid(); d.Next() {
		if w == nil {
			fileNum = db.nextFileNum
			db.nextFileNum++
			path = filepath.Join(db.dir, sstable.FileName(fileNum))
			var err error
			if w, err = sstable.NewWriter(path); err != nil {
				return err
			}
		}
		if err := w.Add(d.InternalKey(), d.Value()); err != nil {
			return err
		}
	}

	if err := d.Error(); err != nil {
		return err
	}

	edit := manifest.VersionEdit{
		DelFiles:    slices.Clone(db.version.Files[0]),
		NextFileNum: db.nextFileNum,
		LastSeq:     db.nextSeq - 1,
	}

	var newL0 []*sstable.Table
	if w != nil {
		if err := w.Finish(); err != nil {
			return err
		}
		sst, err := sstable.Open(path)
		if err != nil {
			return err
		}
		newL0 = append(newL0, sst)
		edit.AddFiles = append(edit.AddFiles, manifest.FileMeta{Level: 0, FileNum: fileNum})
	}

	if err := db.manifest.Append(edit); err != nil {
		return fmt.Errorf("compact: append to manifest: %w", err)
	}
	db.version = db.version.Apply(edit)
	old := db.l0
	db.l0 = newL0
	db.compactions++

	// Deleting an open file is fine on unix: a scan that pinned an old table
	// keeps reading it, and its last Unref closes it.
	for _, t := range old {
		_ = t.Unref()
	}
	for _, f := range edit.DelFiles {
		_ = os.Remove(filepath.Join(db.dir, sstable.FileName(f.FileNum)))
	}
	return nil
}
