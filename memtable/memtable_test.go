package memtable

import (
	"fmt"
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
	require.Equal(t, uint64(len("cat")+8+len("purr")), mt.approxSize)

	require.NoError(t, mt.Put([]byte("cat"), []byte("meow")))
	require.Equal(t, uint64(2*(len("cat")+8+len("meow"))), mt.approxSize,
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
		wantFound bool
	}{
		{"newest version wins", "cat", "meow", true},
		{"tombstone reads as absent", "dog", "", false},
		{"empty value is not a tombstone", "empty", "", true},
		{"user key longer than a trailer", "elephantine", "big", true},
		{"absent key sorting before every entry", "bee", "", false},
		{"absent key that a stored key prefixes", "cats", "", false},
		{"absent key past the last entry", "zebra", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, found := mt.Get([]byte(tc.key))
			require.Equal(t, tc.wantFound, found)
			require.Equal(t, tc.wantVal, string(val))
		})
	}
}

// Seek parks the cursor past the end when nothing matches; Get must notice
// rather than dereference it.
func TestGetOnEmptyMemtable(t *testing.T) {
	mt, _ := newMemtable(t)
	defer mt.Close()

	val, found := mt.Get([]byte("cat"))
	require.False(t, found)
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
				val, found := mt.Get([]byte("cat"))
				require.True(t, found, "cat is written before the readers start and never deleted")
				require.NotEmpty(t, val)
			}
		})
	}
	wg.Wait()
}

func TestGetOnClosedMemtable(t *testing.T) {
	mt, _ := newMemtable(t)
	require.NoError(t, mt.Put([]byte("cat"), []byte("purr")))
	require.NoError(t, mt.Close())

	val, found := mt.Get([]byte("cat"))
	require.False(t, found, "a closed memtable answers nothing, even for data still in memory")
	require.Nil(t, val)
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
	val, found := mt2.Get([]byte("cat"))
	require.True(t, found)
	require.Equal(t, "meow", string(val))

	// a key written once and never touched again
	val, found = mt2.Get([]byte("elephantine"))
	require.True(t, found)
	require.Equal(t, "big", string(val))

	// empty value is a value: found, and distinct from a miss
	val, found = mt2.Get([]byte("empty"))
	require.True(t, found, "an empty value is stored data, not an absent key")
	require.Empty(t, val)

	// the tombstone survived the crash: dog stays deleted even though its
	// Put is still sitting in the WAL ahead of the Delete
	val, found = mt2.Get([]byte("dog"))
	require.False(t, found, "a Delete replayed after its Put must still win")
	require.Nil(t, val)

	// a key that was never written stays missing
	val, found = mt2.Get([]byte("ghost"))
	require.False(t, found)
	require.Nil(t, val)

	// seqs resume past the highest on disk (6 writes => seqs 0..5)
	require.Equal(t, uint64(6), mt2.nextSeq, "replay must resume after the highest seq on disk")

	// and a post-crash write shadows the replayed one
	require.NoError(t, mt2.Put([]byte("cat"), []byte("kebap")))
	val, found = mt2.Get([]byte("cat"))
	require.True(t, found)
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
	sizeBeforeCrash := mt.approxSize

	mt2 := openMemtable(t, path)
	defer mt2.Close()

	require.Equal(t, sizeBeforeCrash, mt2.approxSize,
		"replay must rebuild the size counter the same way mutate accumulates it")
	require.NotZero(t, mt2.approxSize)
}

// New creates the WAL file before the first Put, so a crash in that window
// leaves a real, empty, uncorrupted WAL on disk.
func TestReplayFromEmptyWAL(t *testing.T) {
	_, path := newMemtable(t)

	mt2 := openMemtable(t, path)
	defer mt2.Close()

	require.Equal(t, uint64(0), mt2.nextSeq, "an empty WAL must resume exactly where New starts")
	require.Equal(t, uint64(0), mt2.approxSize)

	_, found := mt2.Get([]byte("cat"))
	require.False(t, found)

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

	_, found := mt3.Get([]byte("cat"))
	require.False(t, found, "a tombstone written in session 2 must outlive session 2")

	val, found := mt3.Get([]byte("dog"))
	require.True(t, found)
	require.Equal(t, "woof", string(val))

	require.Equal(t, uint64(3), mt3.nextSeq, "seqs run across sessions, they do not restart")

	entries, err := wal.Replay(path)
	require.NoError(t, err)
	require.Len(t, entries, 3, "every session appends to the same WAL")
}
