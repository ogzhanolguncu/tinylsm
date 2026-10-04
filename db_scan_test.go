package tinylsm

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scanAll collects Scan output as "k=v" lines.
func scanAll(t *testing.T, db *DB, from, to string) []string {
	t.Helper()
	var out []string
	err := db.Scan([]byte(from), []byte(to), func(k, v []byte) bool {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
		return true
	})
	require.NoError(t, err)
	return out
}

// oracleRange is what Scan must return: the model map, sorted, cut to [from, to).
func oracleRange(model map[string]string, from, to string) []string {
	var out []string
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range model {
			if !yield(k) {
				return
			}
		}
	}) {
		if k >= from && (to == "" || k < to) {
			out = append(out, k+"="+model[k])
		}
	}
	return out
}

// Real memtable + many real L0 tables, overwrites and tombstones landing in
// different tables than the values they shadow, checked against a map —
// before and after a reopen.
func TestScanMatchesOracleAcrossFlushes(t *testing.T) {
	for seed := range uint64(3) {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			dir := t.TempDir()
			opts := Options{MemtableThreshold: 256, L0CompactionTrigger: -1}
			db, err := Open(dir, opts)
			require.NoError(t, err)

			model := map[string]string{}
			for i := range 300 {
				k := fmt.Sprintf("k%03d", rng.IntN(60)) // few keys → many versions
				if rng.IntN(4) == 0 {
					require.NoError(t, db.Delete([]byte(k)))
					delete(model, k)
				} else {
					v := fmt.Sprintf("v%d", i)
					require.NoError(t, db.Put([]byte(k), []byte(v)))
					model[k] = v
				}
			}
			require.Greater(t, len(db.Stats().L0), 5, "test must span many tables to mean anything")
			require.NotZero(t, db.Stats().MemBytes, "and the memtable must hold some of it")

			check := func(label string) {
				require.Equal(t, oracleRange(model, "", ""), scanAll(t, db, "", ""), "%s: full scan", label)
				for range 50 {
					a := fmt.Sprintf("k%03d", rng.IntN(65))
					b := fmt.Sprintf("k%03d", rng.IntN(65))
					require.Equal(t, oracleRange(model, a, b), scanAll(t, db, a, b), "%s: [%s,%s)", label, a, b)
				}
			}
			check("live")

			require.NoError(t, db.Close())
			db, err = Open(dir, opts)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			check("reopened")
		})
	}
}

func TestScanStopsWhenCallbackSaysSo(t *testing.T) {
	db, _ := newDBWith(t, Options{})
	for _, k := range []string{"a", "b", "c", "d"} {
		require.NoError(t, db.Put([]byte(k), []byte(strings.ToUpper(k))))
	}
	var seen []string
	require.NoError(t, db.Scan(nil, nil, func(k, _ []byte) bool {
		seen = append(seen, string(k))
		return len(seen) < 2
	}))
	require.Equal(t, []string{"a", "b"}, seen)
}

// Writes made while a scan runs must not leak into it. The callback writes
// mid-scan, which also proves the scan isn't holding the lock (it would deadlock).
func TestScanIsASnapshot(t *testing.T) {
	db, _ := newDBWith(t, Options{MemtableThreshold: 128})
	require.NoError(t, db.Put([]byte("alice"), []byte("100")))
	require.NoError(t, db.Put([]byte("bob"), []byte("50")))
	require.NoError(t, db.Put([]byte("carol"), []byte("5")))

	var seen []string
	require.NoError(t, db.Scan(nil, nil, func(k, v []byte) bool {
		seen = append(seen, fmt.Sprintf("%s=%s", k, v))
		if string(k) == "alice" {
			require.NoError(t, db.Put([]byte("bob"), []byte("80")))
			require.NoError(t, db.Delete([]byte("carol")))
			require.NoError(t, db.Put([]byte("dave"), []byte("new")))
		}
		return true
	}))
	require.Equal(t, []string{"alice=100", "bob=50", "carol=5"}, seen, "scan sees the DB as of its start")
	require.Equal(t, []string{"alice=100", "bob=80", "dave=new"}, scanAll(t, db, "", ""), "a new scan sees the writes")
}
