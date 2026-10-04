package tinylsm

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// forceFlush pushes whatever is in the memtable into a new L0 table.
func forceFlush(t *testing.T, db *DB) {
	t.Helper()
	db.mu.Lock()
	defer db.mu.Unlock()
	require.NoError(t, db.freeze())
	require.NoError(t, db.flush())
}

func requireGets(t *testing.T, db *DB, model map[string]string, keySpace int, label string) {
	t.Helper()
	for i := range keySpace {
		k := fmt.Sprintf("k%03d", i)
		val, found, err := db.Get([]byte(k))
		require.NoError(t, err)
		want, ok := model[k]
		require.Equal(t, ok, found, "%s: Get(%s) found", label, k)
		if ok {
			require.Equal(t, want, string(val), "%s: Get(%s)", label, k)
		}
	}
}

func TestCompactMatchesOracle(t *testing.T) {
	for seed := range uint64(3) {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 9))
			dir := t.TempDir()
			opts := Options{MemtableThreshold: 256}
			db, err := Open(dir, opts)
			require.NoError(t, err)

			model := map[string]string{}
			for i := range 300 {
				k := fmt.Sprintf("k%03d", rng.IntN(60))
				if rng.IntN(4) == 0 {
					require.NoError(t, db.Delete([]byte(k)))
					delete(model, k)
				} else {
					v := fmt.Sprintf("v%d", i)
					require.NoError(t, db.Put([]byte(k), []byte(v)))
					model[k] = v
				}
			}
			require.Greater(t, len(db.Stats().L0), 5)

			require.NoError(t, db.Compact())
			require.Len(t, db.Stats().L0, 1, "everything folds into one table")
			require.Equal(t, 1, countSSTs(t, dir), "old .sst files are deleted from disk")
			require.Equal(t, oracleRange(model, "", ""), scanAll(t, db, "", ""), "scan after compact")
			requireGets(t, db, model, 60, "after compact")

			require.NoError(t, db.Close())
			db, err = Open(dir, opts)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			require.Len(t, db.Stats().L0, 1, "manifest remembers the compacted table")
			require.Equal(t, oracleRange(model, "", ""), scanAll(t, db, "", ""), "scan after reopen")
			requireGets(t, db, model, 60, "after reopen")

			// seqs must not go backwards: a fresh write must beat compacted data
			require.NoError(t, db.Put([]byte("k000"), []byte("newest")))
			model["k000"] = "newest"
			forceFlush(t, db)
			require.NoError(t, db.Compact())
			requireGets(t, db, model, 60, "write after reopen")
		})
	}
}

// Every entry in the tables is dead: compaction must leave NO table behind,
// not an empty one.
func TestCompactEverythingDeleted(t *testing.T) {
	db, dir := newDBWith(t, Options{})
	require.NoError(t, db.Put([]byte("a"), []byte("1")))
	require.NoError(t, db.Put([]byte("b"), []byte("2")))
	forceFlush(t, db)
	require.NoError(t, db.Delete([]byte("a")))
	require.NoError(t, db.Delete([]byte("b")))
	forceFlush(t, db)
	require.Len(t, db.Stats().L0, 2)

	require.NoError(t, db.Compact())
	require.Empty(t, db.Stats().L0)
	require.Zero(t, countSSTs(t, dir))
	require.Empty(t, scanAll(t, db, "", ""))
	_, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	require.False(t, found)
}

func TestCompactNothingToDo(t *testing.T) {
	db, dir := newDBWith(t, Options{})
	require.NoError(t, db.Compact())
	require.Empty(t, db.Stats().L0)
	require.Zero(t, countSSTs(t, dir))
}
