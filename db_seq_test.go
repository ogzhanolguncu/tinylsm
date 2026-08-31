package tinylsm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/sstable"
	"github.com/stretchr/testify/require"
)

// sstMaxSeq is the highest seq held by any SSTable on disk.
func sstMaxSeq(t *testing.T, dir string) uint64 {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "test needs at least one flushed table")

	high := uint64(0)
	for _, m := range matches {
		tbl, err := sstable.Open(m)
		require.NoError(t, err)
		high = max(high, tbl.MaxSeq())
		require.NoError(t, tbl.Close())
	}
	return high
}

// dropWALs removes every WAL, leaving a directory whose only surviving data is
// in SSTables. That is the state a DB reaches once everything it wrote has been
// flushed and those WALs deleted; recovery must then get its seqnums from the
// tables, because there is nowhere else left to get them.
func dropWALs(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	require.NoError(t, err)
	for _, m := range matches {
		require.NoError(t, os.Remove(m))
	}
}

// THE invariant: every seqnum handed out is strictly greater than every seqnum
// any surviving data already holds — across every reopen, forever. Break it and
// a later write gets a *lower* seq than the write it supersedes, so "highest seq
// wins" silently returns the older value. Get's newest-file-first walk masks it
// today; Phase 7's merge iterator, which orders by seq rather than by file
// position, does not.
func TestReopenRecoversSeqFromSSTablesWhenWALsAreGone(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	fill(t, db, "k", 40)
	require.NoError(t, db.Close())

	require.Greater(t, countSSTs(t, dir), 0, "the fill must have flushed")
	high := sstMaxSeq(t, dir)
	require.Greater(t, high, uint64(0), "flushed tables must carry real seqs")

	dropWALs(t, dir)

	reopened, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	require.Greater(t, reopened.nextSeq, high,
		"the next seq handed out must exceed every seq already on disk (got %d, tables hold %d)",
		reopened.nextSeq, high)
}

// The WAL and the SSTables are two independent lower bounds and recovery takes
// the larger. This is the direction that catches dropping the WAL's own
// contribution while adding the tables'.
func TestReopenSeqIsMaxOfWALAndSSTables(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	fill(t, db, "k", 40)
	// These land in the live memtable after the last flush, so the WAL holds
	// the highest seqs in the directory.
	fill(t, db, "z", 3)
	before := db.nextSeq
	require.NoError(t, db.Close())

	require.Greater(t, before, sstMaxSeq(t, dir), "WAL must hold the highest seqs for this test")
	require.Equal(t, 1, countWALs(t, dir))

	reopened, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	require.Equal(t, before, reopened.nextSeq,
		"recovery must not lose the WAL's high-water mark")
}

// Across repeated reopens that each lose their WAL, the next seq must always
// exceed everything that survived. Note it may legitimately sit *below* the
// pre-close counter: dropping a WAL discards the writes it held, so the
// high-water mark falls back to one above the newest surviving table entry.
// What must never happen is landing at or below a seq that survived, which
// would put two different entries at one seqnum and break both "highest seq
// wins" and the skiplist's distinct-key assumption.
func TestSeqAlwaysExceedsSurvivingDataAcrossRepeatedReopens(t *testing.T) {
	dir := t.TempDir()
	prevSurviving := uint64(0)

	for round := range 3 {
		db, err := Open(dir, Options{MemtableThreshold: tinyThreshold})
		require.NoError(t, err)

		if round > 0 {
			require.Greater(t, db.nextSeq, prevSurviving,
				"round %d: reopen would reuse a seq that survives on disk", round)
		}

		fill(t, db, string(rune('a'+round)), 40)
		require.NoError(t, db.Close())
		dropWALs(t, dir)
		prevSurviving = sstMaxSeq(t, dir)
	}

	require.Greater(t, prevSurviving, uint64(0))
}
