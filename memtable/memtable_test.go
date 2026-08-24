package memtable

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/wal"
	"github.com/stretchr/testify/require"
)

func newMemtable(t *testing.T) (*Memtable, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "000000001.wal")
	mt, err := New(path, 1)
	require.NoError(t, err)
	return mt, path
}

func openMemtable(t *testing.T, path string) *Memtable {
	t.Helper()
	mt, err := Open(path, 1)
	require.NoError(t, err)
	return mt
}

// The WAL is the only thing that survives a crash, so what it holds has to match
// what Put was told: same keys and values, kinds intact, and seqs handed out from
// zero without gaps or repeats.
func TestPutIsDurableInTheWAL(t *testing.T) {
	mt, path := newMemtable(t)

	writes := []struct{ key, val string }{
		{"cat", "purr"},
		{"dog", "woof"},
		{"cat", "meow"}, // overwrite: a second entry, not a replacement
	}
	for _, w := range writes {
		require.NoError(t, mt.Put([]byte(w.key), []byte(w.val)))
	}
	require.NoError(t, mt.Close())

	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, len(writes), "an overwrite must append, not replace")

	for i, w := range writes {
		require.Equal(t, w.key, string(entries[i].Key), "entry %d key", i)
		require.Equal(t, w.val, string(entries[i].Value), "entry %d value", i)
		require.Equal(t, uint64(i), entries[i].Seq, "entry %d seq", i)
		require.Equal(t, wal.KindPut, entries[i].Kind, "entry %d kind", i)
	}
}

func TestPutTracksApproximateSize(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	// internal key is userKey + an 8-byte trailer, plus the raw value
	require.Equal(t, uint64(len("cat")+8+len("purr")), mt.ApproxSize())

	require.NoError(t, mt.Put([]byte("cat"), []byte("meow")))
	require.Equal(t, uint64(2*(len("cat")+8+len("meow"))), mt.ApproxSize(),
		"an overwrite adds to the size, it never replaces")
}

// Concurrent writers share one WAL file and one seq counter. Interleaved writes
// would tear a record and strand every entry after it, and an unlocked counter
// would hand the same seq to two writers.
func TestConcurrentPutsKeepTheWALIntact(t *testing.T) {
	const writers = 50

	mt, path := newMemtable(t)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			require.NoError(t, mt.Put([]byte("cat"), fmt.Appendf(nil, "v%d", i)))
		})
	}
	wg.Wait()
	require.NoError(t, mt.Close())

	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, writers, "a torn record strands everything after it")

	seen := make(map[uint64]bool, writers)
	for _, e := range entries {
		require.Falsef(t, seen[e.Seq], "seq %d handed out twice", e.Seq)
		seen[e.Seq] = true
	}
	require.Len(t, seen, writers)
}

func TestClosedMemtableRejectsWrites(t *testing.T) {
	mt, _ := newMemtable(t)

	require.NoError(t, mt.Close())
	require.ErrorIs(t, mt.Put([]byte("cat"), []byte("purr")), ErrClosed)
	require.ErrorIs(t, mt.Close(), ErrClosed)
}

// entry is one decoded skiplist row: what a reader would actually see.
type entry struct {
	userKey string
	seq     uint64
	kind    keys.Kind
	value   string
}

func skiplistRows(t *testing.T, mt *Memtable) []entry {
	t.Helper()
	var rows []entry
	it := mt.skiplist.NewIterator()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		uk, seq, kind, err := keys.Decode(it.Key())
		require.NoError(t, err)
		rows = append(rows, entry{string(uk), seq, kind, string(it.Value())})
	}
	return rows
}

// The WAL tests say nothing about what is in memory. Without this, a mutate that
// inserts an empty value passes the entire suite.
func TestPutStoresTheValueInTheSkiplist(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))

	require.Equal(t, []entry{{"cat", 0, keys.KindPut, "purr"}}, skiplistRows(t, mt))
}

// A tombstone is an insert, not a removal, and the kind that marks it lives in
// the internal key. If the key said KindPut while the WAL said KindDelete, the
// same key would read differently before and after a reopen.
func TestDeleteWritesATombstone(t *testing.T) {
	mt, path := newMemtable(t)

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Delete([]byte("cat")))

	// newest first: the tombstone shadows the value it replaces
	require.Equal(t, []entry{
		{"cat", 1, keys.KindDelete, ""},
		{"cat", 0, keys.KindPut, "purr"},
	}, skiplistRows(t, mt))

	require.NoError(t, mt.Close())

	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, 2, "a delete appends, it never removes")
	require.Equal(t, wal.KindDelete, entries[1].Kind)
	require.Empty(t, entries[1].Value, "a tombstone carries no value")
}

// No existence check: the key may live in an SSTable this memtable cannot see,
// so the tombstone has to be recorded regardless.
func TestDeleteOfAnAbsentKeyStillWritesATombstone(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	require.NoError(t, mt.Delete([]byte("ghost")))

	require.Equal(t, []entry{{"ghost", 0, keys.KindDelete, ""}}, skiplistRows(t, mt))
}

// Delete then Put resurrects the key, and the resurrection must sort ahead of
// the tombstone or reads would keep seeing the delete.
func TestPutAfterDeleteSortsAheadOfTheTombstone(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Delete([]byte("cat")))
	require.NoError(t, mt.Put([]byte("cat"), []byte("meow")))

	require.Equal(t, []entry{
		{"cat", 2, keys.KindPut, "meow"},
		{"cat", 1, keys.KindDelete, ""},
		{"cat", 0, keys.KindPut, "purr"},
	}, skiplistRows(t, mt))
}

func TestClosedMemtableRejectsDeletes(t *testing.T) {
	mt, _ := newMemtable(t)

	require.NoError(t, mt.Close())
	require.ErrorIs(t, mt.Delete([]byte("cat")), ErrClosed)
}

// One fixture exercising every way Get can be wrong: the wrong version, a
// tombstone, an empty value mistaken for one, and the three overshoot shapes.
func TestGet(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Put([]byte("dog"), []byte("woof")))
	require.NoError(t, mt.Put([]byte("elephantine"), []byte("big")))
	require.NoError(t, mt.Put([]byte("empty"), []byte("")))
	require.NoError(t, mt.Delete([]byte("dog")))
	require.NoError(t, mt.Put([]byte("cat"), []byte("meow")))

	cases := []struct {
		name      string
		key       string
		wantVal   string
		wantState keys.LookupState
	}{
		{"newest version wins", "cat", "meow", keys.Found},
		{"tombstone reads as deleted", "dog", "", keys.Deleted},
		{"empty value is not a tombstone", "empty", "", keys.Found},
		{"user key longer than a trailer", "elephantine", "big", keys.Found},
		{"absent key sorting before every entry", "bee", "", keys.NotFound},
		{"absent key that a stored key prefixes", "cats", "", keys.NotFound},
		{"absent key past the last entry", "zebra", "", keys.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, state := mt.Get([]byte(tc.key))
			require.Equal(t, tc.wantState, state)
			require.Equal(t, tc.wantVal, string(val))
		})
	}
}

// Seek parks the cursor past the end when nothing matches; Get must notice
// rather than dereference it.
func TestGetOnEmptyMemtable(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	val, state := mt.Get([]byte("cat"))
	require.Equal(t, keys.NotFound, state)
	require.Nil(t, val)
}

// Get takes no lock: it relies on the skiplist storing next pointers atomically
// so readers can traverse while a writer splices. Nothing else proves that.
func TestGetIsSafeDuringConcurrentPuts(t *testing.T) {
	const writers, readers = 8, 8

	mt, _ := newMemtable(t)
	defer mt.Close()
	require.NoError(t, mt.Put([]byte("cat"), []byte("v0")))

	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for i := range 100 {
				require.NoError(t, mt.Put([]byte("cat"), fmt.Appendf(nil, "v%d", i+1)))
			}
		})
	}
	for range readers {
		wg.Go(func() {
			for range 100 {
				val, state := mt.Get([]byte("cat"))
				require.Equal(t, keys.Found, state, "cat is written before the readers start and never deleted")
				require.NotEmpty(t, val)
			}
		})
	}
	wg.Wait()
}

// Close shuts the WAL writer, not the skiplist. A frozen memtable is still
// being read while its flush writes it out, so Get must keep answering from
// memory long after the last write is refused.
func TestGetOnClosedMemtable(t *testing.T) {
	mt, _ := newMemtable(t)
	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Close())

	val, state := mt.Get([]byte("cat"))
	require.Equal(t, keys.Found, state, "closing stops writes, it does not hide data still in memory")
	require.Equal(t, "purr", string(val))

	require.ErrorIs(t, mt.Put([]byte("cat"), []byte("meow")), ErrClosed)
}

// A crash is just "Close never ran". Everything Put returned success for is
// already fsynced, so reopening has to reconstruct the exact same view: latest
// value per key, tombstones still hiding what they hid, and seqs resuming past
// the highest one on disk so post-crash writes never tie with pre-crash ones.
func TestReplayFromCrash(t *testing.T) {
	mt, path := newMemtable(t)

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Put([]byte("dog"), []byte("woof")))
	require.NoError(t, mt.Put([]byte("elephantine"), []byte("big")))
	require.NoError(t, mt.Put([]byte("empty"), []byte("")))
	require.NoError(t, mt.Delete([]byte("dog")))
	require.NoError(t, mt.Put([]byte("cat"), []byte("meow")))

	// crash: process dies here. no Close, no final sync, no cleanup.
	// mt is dead from this line on.
	mt = nil
	_ = mt

	mt2 := openMemtable(t, path)
	defer mt2.Close()

	// an overwritten key replays to its LAST value, not its first
	val, state := mt2.Get([]byte("cat"))
	require.Equal(t, keys.Found, state)
	require.Equal(t, "meow", string(val))

	// a key written once and never touched again
	val, state = mt2.Get([]byte("elephantine"))
	require.Equal(t, keys.Found, state)
	require.Equal(t, "big", string(val))

	// empty value is a value: found, and distinct from a miss
	val, state = mt2.Get([]byte("empty"))
	require.Equal(t, keys.Found, state, "an empty value is stored data, not an absent key")
	require.Empty(t, val)

	// the tombstone survived the crash: dog stays deleted even though its
	// Put is still sitting in the WAL ahead of the Delete
	val, state = mt2.Get([]byte("dog"))
	require.Equal(t, keys.Deleted, state, "a Delete replayed after its Put must still win")
	require.Nil(t, val)

	// a key that was never written stays missing
	val, state = mt2.Get([]byte("ghost"))
	require.Equal(t, keys.NotFound, state)
	require.Nil(t, val)

	// seqs resume past the highest on disk (6 writes => seqs 0..5)
	require.Equal(t, uint64(6), mt2.nextSeq, "replay must resume after the highest seq on disk")

	// and a post-crash write shadows the replayed one
	require.NoError(t, mt2.Put([]byte("cat"), []byte("kebap")))
	val, state = mt2.Get([]byte("cat"))
	require.Equal(t, keys.Found, state)
	require.Equal(t, "kebap", string(val))
	require.Equal(t, uint64(7), mt2.nextSeq)

	// post-crash writes land in the same WAL, appended after the old ones
	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, 7, "reopening appends to the WAL, it does not restart it")
	require.Equal(t, uint64(6), entries[6].Seq)
	require.Equal(t, "cat", string(entries[6].Key))
	require.Equal(t, "kebap", string(entries[6].Value))
}

// approxSize drives the flush trigger. A reopened memtable that reports zero
// would never flush and grow past its threshold unbounded.
func TestReplayRestoresApproximateSize(t *testing.T) {
	mt, path := newMemtable(t)

	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Delete([]byte("dog")))
	require.NoError(t, mt.Put([]byte("elephantine"), []byte("big")))
	sizeBeforeCrash := mt.ApproxSize()

	mt2 := openMemtable(t, path)
	defer mt2.Close()

	require.Equal(t, sizeBeforeCrash, mt2.ApproxSize(),
		"replay must rebuild the size counter the same way mutate accumulates it")
	require.NotZero(t, mt2.ApproxSize())
}

// New creates the WAL file before the first Put, so a crash in that window
// leaves a real, empty, uncorrupted WAL on disk.
func TestReplayFromEmptyWAL(t *testing.T) {
	_, path := newMemtable(t)

	mt2 := openMemtable(t, path)
	defer mt2.Close()

	require.Equal(t, uint64(0), mt2.nextSeq, "an empty WAL must resume exactly where New starts")
	require.Equal(t, uint64(0), mt2.ApproxSize())

	_, state := mt2.Get([]byte("cat"))
	require.Equal(t, keys.NotFound, state)

	// the reopened memtable is writable, and its first seq is still 0
	require.NoError(t, mt2.Put([]byte("cat"), []byte("purr")))
	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(0), entries[0].Seq)
}

// One reopen can look correct while still truncating the file: session 2's own
// writes replay fine because they are all that is left. Only a third session
// proves the WAL is appended to and never restarted.
func TestReplayAcrossTwoReopens(t *testing.T) {
	mt, path := newMemtable(t)
	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))

	mt2 := openMemtable(t, path)
	require.NoError(t, mt2.Put([]byte("dog"), []byte("woof")))
	require.NoError(t, mt2.Delete([]byte("cat")))

	mt3 := openMemtable(t, path)
	defer mt3.Close()

	_, state := mt3.Get([]byte("cat"))
	require.Equal(t, keys.Deleted, state, "a tombstone written in session 2 must outlive session 2")

	val, state := mt3.Get([]byte("dog"))
	require.Equal(t, keys.Found, state)
	require.Equal(t, "woof", string(val))

	require.Equal(t, uint64(3), mt3.nextSeq, "seqs run across sessions, they do not restart")

	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, 3, "every session appends to the same WAL")
}

// The hand-written tests each pin one behavior. This one looks for the
// combination none of them thought of: a delete of a key that was overwritten
// three sessions ago, a rewrite of a key deleted just before a reopen, and so
// on. The oracle is a plain map, which never crashes and never replays, so any
// divergence is the memtable's.
func TestRandomOpsMatchAMapOracleAcrossReopens(t *testing.T) {
	const (
		ops         = 1200 // every mutating op fsyncs, so this is wall-clock bound
		keyspace    = 40   // small, so overwrites and deletes actually collide
		reopenEvery = 61
	)

	rng := rand.New(rand.NewPCG(1, 2))
	key := func(i int) []byte { return fmt.Appendf(nil, "key%02d", i) }

	// present-in-the-map == live key; absent == never written or tombstoned.
	// Get now distinguishes Deleted from NotFound, so this oracle no longer
	// checks everything it could: tracking tombstones as a third state would.
	oracle := make(map[string]string)

	checkAll := func(mt *Memtable, when string) {
		t.Helper()
		for i := range keyspace {
			k := key(i)
			val, state := mt.Get(k)
			want, live := oracle[string(k)]
			require.Equal(t, live, state == keys.Found, "%s: %q liveness", when, k)
			if live {
				require.Equal(t, want, string(val), "%s: %q value", when, k)
			}
		}
	}

	mt, path := newMemtable(t)
	defer func() { _ = mt.Close() }()

	for i := range ops {
		k := key(rng.IntN(keyspace))

		switch n := rng.IntN(100); {
		case n < 55:
			val := fmt.Appendf(nil, "v%d", i)
			require.NoError(t, mt.Put(k, val))
			oracle[string(k)] = string(val)
		case n < 70:
			// empty value: still a live key, and a different code path than a delete
			require.NoError(t, mt.Put(k, nil))
			oracle[string(k)] = ""
		case n < 90:
			require.NoError(t, mt.Delete(k))
			delete(oracle, string(k))
		default:
			val, state := mt.Get(k)
			want, live := oracle[string(k)]
			require.Equal(t, live, state == keys.Found, "op %d: %q liveness", i, k)
			if live {
				require.Equal(t, want, string(val), "op %d: %q value", i, k)
			}
		}

		if i > 0 && i%reopenEvery == 0 {
			// crash: the old memtable is abandoned unclosed, its fd left dangling,
			// exactly as a killed process would leave it.
			mt = openMemtable(t, path)
			checkAll(mt, fmt.Sprintf("after reopen at op %d", i))
		}
	}

	checkAll(mt, "final")

	// the WAL only ever grew: one record per mutating op, seqs dense from zero
	entries, err := wal.Replay(path)
	require.NoError(t, err)
	for i, e := range entries {
		require.Equal(t, uint64(i), e.Seq, "record %d", i)
	}
	require.Equal(t, uint64(len(entries)), mt.nextSeq)
}
