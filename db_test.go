package tinylsm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/memtable"
	"github.com/ogzhanolguncu/tinylsm/wal"
	"github.com/stretchr/testify/require"
)

func newDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

// walPath is where a fresh DB puts its one and only WAL. Tests that read the
// log directly need to agree with Open about the name.
func walPath(dir string) string { return filepath.Join(dir, wal.FileName(1)) }

func TestPutThenGet(t *testing.T) {
	db, _ := newDB(t)

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "purr", string(val))
}

func TestGetOfANeverWrittenKey(t *testing.T) {
	db, _ := newDB(t)

	val, found, err := db.Get([]byte("ghost"))
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, val)
}

// An overwrite is a second entry at a higher seq, not a replacement. If DB
// handed out the same seq twice, the two versions would be byte-identical
// internal keys and the newer one could not win.
func TestOverwriteReturnsTheNewerValue(t *testing.T) {
	db, dir := newDB(t)

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, db.Put([]byte("cat"), []byte("meow")))

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "meow", string(val))

	require.NoError(t, db.Close())
	entries, err := wal.Replay(walPath(dir))
	require.NoError(t, err)
	require.Len(t, entries, 2, "an overwrite appends")
	require.Equal(t, uint64(0), entries[0].Seq)
	require.Equal(t, uint64(1), entries[1].Seq)
}

func TestDeleteHidesTheKey(t *testing.T) {
	db, _ := newDB(t)

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, db.Delete([]byte("cat")))

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.False(t, found, "a tombstone reads as absent at the DB level")
	require.Nil(t, val)
}

// No existence check on the way in: the key may live in an SSTable this DB has
// not consulted yet, so the tombstone is recorded regardless.
func TestDeleteOfAnAbsentKeyIsNotAnError(t *testing.T) {
	db, dir := newDB(t)

	require.NoError(t, db.Delete([]byte("ghost")))
	require.NoError(t, db.Close())

	entries, err := wal.Replay(walPath(dir))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, wal.KindDelete, entries[0].Kind)
}

func TestPutAfterDeleteResurrectsTheKey(t *testing.T) {
	db, _ := newDB(t)

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, db.Delete([]byte("cat")))
	require.NoError(t, db.Put([]byte("cat"), []byte("meow")))

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "meow", string(val))
}

// An empty value is stored data. Collapsing it into "absent" is the classic
// way a KV loses the difference between a written empty string and a miss.
func TestEmptyValueIsFound(t *testing.T) {
	db, _ := newDB(t)

	require.NoError(t, db.Put([]byte("empty"), []byte("")))

	val, found, err := db.Get([]byte("empty"))
	require.NoError(t, err)
	require.True(t, found, "an empty value is not a tombstone")
	require.Empty(t, val)
}

// Reopen has to rebuild the view AND the counter. A DB that restarts seqs at
// zero looks correct until the next overwrite ties with a pre-close write.
func TestReopenRestoresDataAndResumesSeqs(t *testing.T) {
	db, dir := newDB(t)

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, db.Put([]byte("dog"), []byte("woof")))
	require.NoError(t, db.Delete([]byte("dog")))
	require.NoError(t, db.Close())

	db2, err := Open(dir, Options{})
	require.NoError(t, err)
	defer db2.Close()

	val, found, err := db2.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "purr", string(val))

	_, found, err = db2.Get([]byte("dog"))
	require.NoError(t, err)
	require.False(t, found, "a tombstone written before Close must outlive it")

	// the write after reopen must not tie with anything already on disk
	require.NoError(t, db2.Put([]byte("cat"), []byte("meow")))
	val, found, err = db2.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "meow", string(val), "the post-reopen write has to win")

	entries, err := wal.Replay(walPath(dir))
	require.NoError(t, err)
	require.Len(t, entries, 4, "reopening appends to the same WAL")
	for i, e := range entries {
		require.Equal(t, uint64(i), e.Seq, "record %d: seqs run across sessions", i)
	}
}

// Two reopens, because one can look right while still truncating: session 2's
// writes replay fine when they are all that survived.
func TestTwoReopens(t *testing.T) {
	db, dir := newDB(t)
	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, db.Close())

	db2, err := Open(dir, Options{})
	require.NoError(t, err)
	require.NoError(t, db2.Put([]byte("dog"), []byte("woof")))
	require.NoError(t, db2.Delete([]byte("cat")))
	require.NoError(t, db2.Close())

	db3, err := Open(dir, Options{})
	require.NoError(t, err)
	defer db3.Close()

	_, found, err := db3.Get([]byte("cat"))
	require.NoError(t, err)
	require.False(t, found)

	val, found, err := db3.Get([]byte("dog"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "woof", string(val))
}

// Only milestone 4 creates a second WAL. Until then two of them means the
// directory is in a state Open cannot reason about, and picking one would
// silently drop whatever is in the other.
// Two WALs on disk = crash between freeze and flush. Both must be recovered,
// and the newer WAL's value must win.
func TestOpenRecoversTwoWALs(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, Options{})
	require.NoError(t, err)
	require.NoError(t, db.Put([]byte("a"), []byte("old")))
	require.NoError(t, db.Put([]byte("b"), []byte("only-old")))
	require.NoError(t, db.Close())

	mt, err := memtable.New(filepath.Join(dir, wal.FileName(100)), skiplistSeed)
	require.NoError(t, err)
	require.NoError(t, mt.Put([]byte("a"), []byte("new"), 50))
	require.NoError(t, mt.Close())

	db, err = Open(dir, Options{})
	require.NoError(t, err)
	defer db.Close()
	for k, want := range map[string]string{"a": "new", "b": "only-old"} {
		v, ok, err := db.Get([]byte(k))
		require.NoError(t, err)
		require.True(t, ok, k)
		require.Equal(t, want, string(v), k)
	}

	wals, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	require.NoError(t, err)
	require.Len(t, wals, 1)
}

func TestOpenCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "lsm")

	db, err := Open(dir, Options{})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	require.FileExists(t, walPath(dir))
}

func TestClosedDBRejectsWrites(t *testing.T) {
	db, _ := newDB(t)

	require.NoError(t, db.Close())
	require.Error(t, db.Put([]byte("cat"), []byte("purr")))
	require.Error(t, db.Delete([]byte("cat")))
}

// db.nextSeq is read, handed down, and bumped as one step. Without DB's lock
// two writers can read the same value and write two versions of a key at the
// same seq — byte-identical internal keys, which the skiplist refuses.
func TestConcurrentWritesGetDistinctSeqs(t *testing.T) {
	const writers = 50

	db, dir := newDB(t)

	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			require.NoError(t, db.Put([]byte("cat"), fmt.Appendf(nil, "v%d", i)))
		})
	}
	wg.Wait()
	require.NoError(t, db.Close())

	entries, err := wal.Replay(walPath(dir))
	require.NoError(t, err)
	require.Len(t, entries, writers)

	seen := make(map[uint64]bool, writers)
	for _, e := range entries {
		require.Falsef(t, seen[e.Seq], "seq %d handed out twice", e.Seq)
		seen[e.Seq] = true
	}
	require.Len(t, seen, writers, "seqs must be dense, not just distinct")
}

func TestOpenRejectsMalformedFileNames(t *testing.T) {
	for _, name := range []string{"x.wal", "12.wal", "0000000001.wal"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o644))

			_, err := Open(dir, Options{})
			require.ErrorIs(t, err, ErrUnknownFileName)
		})
	}
}

func TestReopenKeepsOneManifest(t *testing.T) {
	dir := t.TempDir()
	for range 3 {
		db, err := Open(dir, Options{})
		require.NoError(t, err)
		require.NoError(t, db.Close())
	}
	m, err := filepath.Glob(filepath.Join(dir, "MANIFEST-*"))
	require.NoError(t, err)
	require.Len(t, m, 1)
}

func TestOpenDeletesOrphanSSTables(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "000000099.sst")
	require.NoError(t, os.WriteFile(orphan, nil, 0o644))

	db, err := Open(dir, Options{})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoFileExists(t, orphan)
}

// NoSync trades power-loss durability for speed, but a write must still reach
// the WAL: a clean reopen (no flush ever happened) has to see it.
func TestNoSyncWritesStillReachTheWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, Options{NoSync: true, MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	require.NoError(t, db.Put([]byte("k"), []byte("v")))
	first := db.mem
	require.True(t, db.mem.NoSync(), "option must reach the live WAL writer")
	fill(t, db, "pad", 20)
	require.NotSame(t, first, db.mem, "the fill must have frozen the memtable")
	require.True(t, db.mem.NoSync(), "and every memtable after a freeze")
	require.NoError(t, db.Close())

	db, err = Open(dir, Options{})
	require.NoError(t, err)
	defer db.Close()
	val, found, err := db.Get([]byte("k"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v", string(val))
}
