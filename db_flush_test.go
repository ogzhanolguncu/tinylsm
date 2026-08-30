package tinylsm

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A threshold small enough that a handful of writes crosses it, so a test can
// force flush boundaries without writing megabytes.
const tinyThreshold = 64

func newDBWith(t *testing.T, opts Options) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func countSSTs(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	require.NoError(t, err)
	return len(matches)
}

func countWALs(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	require.NoError(t, err)
	return len(matches)
}

// fill writes enough distinct keys to push the memtable past a tiny threshold.
func fill(t *testing.T, db *DB, prefix string, n int) {
	t.Helper()
	for i := range n {
		require.NoError(t, db.Put(fmt.Appendf(nil, "%s%03d", prefix, i), []byte("filler-value")))
	}
}

func TestFlushProducesAnSSTable(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.Equal(t, 0, countSSTs(t, dir), "nothing flushed before the threshold is crossed")

	fill(t, db, "k", 20)

	require.Greater(t, countSSTs(t, dir), 0, "crossing the threshold must produce L0 files")
}

// Repeated crossings must each cut their own file. One file means freeze is
// reusing the same memtable or the same file number.
func TestRepeatedFlushesProduceDistinctFiles(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	fill(t, db, "a", 20)
	first := countSSTs(t, dir)
	require.Greater(t, first, 0)

	fill(t, db, "b", 20)
	require.Greater(t, countSSTs(t, dir), first, "the second batch needs its own table")
}

// A flushed memtable's WAL is dead weight: the SSTable owns that data now.
func TestFlushDropsTheOldWAL(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	fill(t, db, "k", 20)

	require.Equal(t, 1, countWALs(t, dir), "only the live memtable keeps a WAL")
}

// The core L0 ordering bug: the same user key written on both sides of a flush
// boundary. Searching L0 oldest-first, or searching L0 before the memtable,
// returns "v1" here.
func TestOverwriteAcrossAFlushBoundary(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("v1")))
	fill(t, db, "pad", 20)
	require.NoError(t, db.Put([]byte("cat"), []byte("v2")))

	require.Greater(t, countSSTs(t, dir), 0, "the test is pointless without a real flush")

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v2", string(val), "the newer version wins regardless of where it lives")
}

// Same bug, one level deeper: both versions are on disk, in different tables.
func TestOverwriteAcrossTwoFlushBoundaries(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("v1")))
	fill(t, db, "pad", 20)
	require.NoError(t, db.Put([]byte("cat"), []byte("v2")))
	fill(t, db, "qad", 20)
	require.NoError(t, db.Put([]byte("cat"), []byte("v3")))

	require.GreaterOrEqual(t, countSSTs(t, dir), 2)

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v3", string(val))
}

// PLAN.md's Phase 5 trap: the tombstone lives only in the memtable, the value
// only in an SSTable. The read path has to stop at the first hit, not keep
// looking until it finds a PUT.
func TestTombstoneInMemtableShadowsAnSSTableValue(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	fill(t, db, "pad", 20)
	require.Greater(t, countSSTs(t, dir), 0)

	require.NoError(t, db.Delete([]byte("cat")))

	_, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.False(t, found, "a tombstone above a flushed value still hides it")
}

// The tombstone itself gets flushed. It must keep shadowing the older value in
// the older table — deletes are not garbage until compaction says so.
func TestTombstoneSurvivesItsOwnFlush(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	fill(t, db, "pad", 20)
	require.NoError(t, db.Delete([]byte("cat")))
	fill(t, db, "qad", 20)

	require.GreaterOrEqual(t, countSSTs(t, dir), 2)

	_, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.False(t, found)
}

// Resurrection after a flushed tombstone: PUT > DELETE > PUT spread over three
// tables. Wrong ordering here reads as "still deleted".
func TestPutAfterAFlushedTombstone(t *testing.T) {
	db, _ := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("purr")))
	fill(t, db, "pad", 20)
	require.NoError(t, db.Delete([]byte("cat")))
	fill(t, db, "qad", 20)
	require.NoError(t, db.Put([]byte("cat"), []byte("meow")))

	val, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "meow", string(val))
}

// Everything above, re-read by a fresh DB. Reopen has to load the tables in an
// order that preserves the same answers — and recover a seq past all of them.
func TestReopenPreservesFlushOrdering(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	require.NoError(t, db.Put([]byte("cat"), []byte("v1")))
	require.NoError(t, db.Put([]byte("dog"), []byte("woof")))
	fill(t, db, "pad", 20)
	require.NoError(t, db.Put([]byte("cat"), []byte("v2")))
	require.NoError(t, db.Delete([]byte("dog")))
	fill(t, db, "qad", 20)
	require.NoError(t, db.Close())

	db2, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	defer db2.Close()

	val, found, err := db2.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v2", string(val))

	_, found, err = db2.Get([]byte("dog"))
	require.NoError(t, err)
	require.False(t, found, "a flushed tombstone outlives the process")

	// a post-reopen write must outrank everything already in the tables
	require.NoError(t, db2.Put([]byte("cat"), []byte("v3")))
	val, found, err = db2.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v3", string(val), "recovered seq has to exceed the flushed ones")
}

// Keys that were only ever written before a flush are still readable; the pad
// keys are the bulk of what got flushed.
func TestFlushedKeysStayReadable(t *testing.T) {
	db, dir := newDBWith(t, Options{MemtableThreshold: tinyThreshold})

	fill(t, db, "pad", 20)
	require.Greater(t, countSSTs(t, dir), 0)

	for i := range 20 {
		val, found, err := db.Get(fmt.Appendf(nil, "pad%03d", i))
		require.NoError(t, err)
		require.Truef(t, found, "pad%03d went missing across the flush", i)
		require.Equal(t, "filler-value", string(val))
	}
}

// Threshold 0 means "default", not "flush on every write" — otherwise every
// existing test in db_test.go silently starts cutting SSTables.
func TestZeroThresholdUsesTheDefault(t *testing.T) {
	db, dir := newDBWith(t, Options{})

	fill(t, db, "k", 50)

	require.Equal(t, 0, countSSTs(t, dir), "50 small writes are nowhere near the default threshold")
}
