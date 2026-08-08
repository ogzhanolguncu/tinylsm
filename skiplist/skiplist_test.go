package skiplist

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const seed = 42

func newList() *SkipList {
	return New(bytes.Compare, seed)
}

func TestInsertGet(t *testing.T) {
	s := newList()
	s.Insert([]byte("foo"), []byte("bar"))

	got, ok := s.Get([]byte("foo"))
	require.True(t, ok, "Get(foo)")
	require.Equal(t, "bar", string(got))
}

func TestGetMissing(t *testing.T) {
	s := newList()
	s.Insert([]byte("foo"), []byte("bar"))

	_, ok := s.Get([]byte("nope"))
	require.False(t, ok, "Get(nope)")
}

func TestGetEmpty(t *testing.T) {
	s := newList()
	_, ok := s.Get([]byte("foo"))
	require.False(t, ok, "Get on empty list")
}

func TestOverwrite(t *testing.T) {
	s := newList()
	s.Insert([]byte("k"), []byte("v1"))
	s.Insert([]byte("k"), []byte("v2"))

	got, ok := s.Get([]byte("k"))
	require.True(t, ok, "Get(k)")
	require.Equal(t, "v2", string(got), "want latest value")
}

// walkKeys returns level-0 keys in order (same-package access to internals).
func walkKeys(s *SkipList) [][]byte {
	var out [][]byte
	for n := s.head.fp[0].Load(); n != nil; n = n.fp[0].Load() {
		out = append(out, n.key)
	}
	return out
}

func TestSortedOrder(t *testing.T) {
	s := newList()
	// insert out of order
	for _, k := range []string{"m", "a", "z", "c", "b"} {
		s.Insert([]byte(k), []byte(k))
	}

	keys := walkKeys(s)
	for i := 1; i < len(keys); i++ {
		require.Lessf(t, bytes.Compare(keys[i-1], keys[i]), 0,
			"not sorted at %d: %q then %q", i, keys[i-1], keys[i])
	}
}

func TestManyInserts(t *testing.T) {
	s := newList()
	const n = 1000
	for i := range n {
		k := fmt.Appendf(nil, "key%05d", i)
		s.Insert(k, k)
	}
	for i := range n {
		k := fmt.Appendf(nil, "key%05d", i)
		got, ok := s.Get(k)
		require.Truef(t, ok, "Get(%q)", k)
		require.Equalf(t, string(k), string(got), "Get(%q)", k)
	}
	require.Len(t, walkKeys(s), n, "level-0 count")
}

// TestIterator_Oracle: skiplist iterator vs a map+sorted-slice oracle.
func TestIterator_Oracle(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewSource(seed))

	s := newList()
	oracle := map[string][]byte{}
	for len(oracle) < n {
		kb := make([]byte, 1+rng.Intn(8))
		rng.Read(kb)
		k := string(kb)
		if _, dup := oracle[k]; dup {
			continue // plan assumes distinct keys
		}
		v := fmt.Appendf(nil, "val-%d", len(oracle))
		oracle[k] = v
		s.Insert(kb, v)
	}

	sorted := make([]string, 0, len(oracle))
	for k := range oracle {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	// 1. full walk == sorted order, strictly ascending.
	it := s.NewIterator()
	i := 0
	var prev []byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		require.Lessf(t, i, len(sorted), "iterator yielded more than %d entries", len(sorted))
		if prev != nil {
			require.Lessf(t, bytes.Compare(prev, it.Key()), 0,
				"not strictly ascending at %d: %q then %q", i, prev, it.Key())
		}
		require.Equalf(t, sorted[i], string(it.Key()), "walk[%d] key", i)
		require.Equalf(t, string(oracle[sorted[i]]), string(it.Value()), "walk[%d] val", i)
		prev = append(prev[:0], it.Key()...)
		i++
	}
	require.Equal(t, len(sorted), i, "walk entry count")

	// 2. exact-match Seek lands on the key, value matches.
	for k, v := range oracle {
		it.Seek([]byte(k))
		require.Truef(t, it.Valid(), "Seek(%q): want hit", k)
		require.Equalf(t, k, string(it.Key()), "Seek(%q) landing key", k)
		require.Equalf(t, string(v), string(it.Value()), "Seek(%q) val", k)
	}

	// 3. between-keys Seek: probe absent keys, expect first entry >= probe.
	for range 1000 {
		pb := make([]byte, 1+rng.Intn(8))
		rng.Read(pb)
		probe := string(pb)

		// oracle answer: first sorted key >= probe.
		idx := sort.SearchStrings(sorted, probe)

		it.Seek(pb)
		if idx == len(sorted) {
			require.Falsef(t, it.Valid(), "Seek(%q): want past-end", probe)
			continue
		}
		require.Truef(t, it.Valid(), "Seek(%q): want %q", probe, sorted[idx])
		require.Equalf(t, sorted[idx], string(it.Key()), "Seek(%q) landing key", probe)
	}
}

func BenchmarkInsert(b *testing.B) {
	rng := rand.New(rand.NewSource(seed))
	keys := make([][]byte, b.N)
	for i := range keys {
		kb := make([]byte, 16)
		rng.Read(kb)
		keys[i] = kb
	}
	s := newList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Insert(keys[i], keys[i])
	}
}

// benchList builds an n-entry list of random 16-byte keys (val == key).
func benchList(n int) (*SkipList, [][]byte) {
	rng := rand.New(rand.NewSource(seed))
	s := newList()
	keys := make([][]byte, n)
	for i := range keys {
		kb := make([]byte, 16)
		rng.Read(kb)
		keys[i] = kb
		s.Insert(kb, kb)
	}
	return s, keys
}

func BenchmarkGet(b *testing.B) {
	s, keys := benchList(1_000_000)
	i := 0
	for b.Loop() {
		if _, ok := s.Get(keys[i%len(keys)]); !ok {
			b.Fatal("miss on inserted key")
		}
		i++
	}
}

// BenchmarkGetMiss: every probe absent (17-byte probes can never equal
// 16-byte inserted keys). Baseline for the bloom-filter phase.
func BenchmarkGetMiss(b *testing.B) {
	s, _ := benchList(1_000_000)
	rng := rand.New(rand.NewSource(seed + 1))
	probes := make([][]byte, 1024)
	for i := range probes {
		pb := make([]byte, 17)
		rng.Read(pb)
		probes[i] = pb
	}
	i := 0
	for b.Loop() {
		if _, ok := s.Get(probes[i%len(probes)]); ok {
			b.Fatal("hit on absent key")
		}
		i++
	}
}

func BenchmarkSeek(b *testing.B) {
	s, keys := benchList(1_000_000)
	it := s.NewIterator()
	i := 0
	for b.Loop() {
		it.Seek(keys[i%len(keys)])
		if !it.Valid() {
			b.Fatal("seek on inserted key: !Valid")
		}
		i++
	}
}

// BenchmarkGetParallel: RLock contention — all cores reading at once.
func BenchmarkGetParallel(b *testing.B) {
	s, keys := benchList(1_000_000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, ok := s.Get(keys[i%len(keys)]); !ok {
				b.Fatal("miss on inserted key")
			}
			i++
		}
	})
}

// BenchmarkGetParallelWithWriter: read throughput on all cores while one
// background writer inserts continuously. The lock-free comparison bench —
// with RWMutex every Insert blocks all readers; lock-free readers never wait.
func BenchmarkGetParallelWithWriter(b *testing.B) {
	s, keys := benchList(1_000_000)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		rng := rand.New(rand.NewSource(seed + 2))
		for {
			select {
			case <-stop:
				return
			default:
			}
			kb := make([]byte, 16)
			rng.Read(kb)
			s.Insert(kb, kb)
		}
	})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, ok := s.Get(keys[i%len(keys)]); !ok {
				b.Fatal("miss on inserted key")
			}
			i++
		}
	})
	b.StopTimer()
	close(stop)
	wg.Wait()
}

// BenchmarkInsertWithReaders: insert latency while background readers hold
// RLock — the other half of the lock-free comparison (writer waiting for
// readers to drain).
func BenchmarkInsertWithReaders(b *testing.B) {
	const numReaders = 4
	s, keys := benchList(1_000_000)

	rng := rand.New(rand.NewSource(seed + 3))
	fresh := make([][]byte, b.N)
	for i := range fresh {
		kb := make([]byte, 16)
		rng.Read(kb)
		fresh[i] = kb
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range numReaders {
		wg.Go(func() {
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.Get(keys[i%len(keys)])
				i++
			}
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Insert(fresh[i], fresh[i])
	}
	b.StopTimer()
	close(stop)
	wg.Wait()
}

func BenchmarkIterate(b *testing.B) {
	const n = 1_000_000
	rng := rand.New(rand.NewSource(seed))
	s := newList()
	for i := 0; i < n; i++ {
		kb := make([]byte, 16)
		rng.Read(kb)
		s.Insert(kb, kb)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it := s.NewIterator()
		count := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			count++
		}
		if count == 0 {
			b.Fatal("iterated zero entries")
		}
	}
}

// Pins the ownership rule: Insert copies key and val, so callers may reuse
// their slices afterwards without mutating the skiplist's contents.
func TestInsertCopiesKeyAndVal(t *testing.T) {
	s := newList()
	key := []byte("cat")
	val := []byte("purr")
	s.Insert(key, val)

	key[0], val[0] = 'X', 'X' // caller reuses its buffers

	got, ok := s.Get([]byte("cat"))
	require.True(t, ok, "key mutated inside skiplist — Insert did not copy key")
	require.Equal(t, "purr", string(got), "Insert did not copy val")

	_, ok = s.Get([]byte("Xat"))
	require.False(t, ok, "skiplist aliases caller's key slice")
}
