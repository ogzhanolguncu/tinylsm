package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentReadersOneWriter: one writer inserting distinct random keys
// while readers concurrently Get published keys and walk the list. Must be
// run under -race (make test-race).
//
// The writer publishes its progress through an atomic counter; a reader that
// observes published=n is guaranteed (atomic store happens-after the insert)
// to find keys[0:n] in the list.
//
// Goroutines use assert (not require): require.FailNow must only run on the
// test goroutine.
func TestConcurrentReadersOneWriter(t *testing.T) {
	const (
		numKeys    = 50_000
		numReaders = 4
	)

	rng := rand.New(rand.NewSource(seed))
	keys := make([][]byte, numKeys)
	for i, p := range rng.Perm(numKeys) {
		keys[i] = fmt.Appendf(nil, "key%08d", p)
	}

	s := newList()
	var published atomic.Int64
	done := make(chan struct{})
	var wg sync.WaitGroup

	// one writer
	wg.Go(func() {
		defer close(done)
		for i, k := range keys {
			s.Insert(k, k)
			published.Store(int64(i + 1))
		}
	})

	// point readers: Get keys the writer has already published
	for r := range numReaders {
		wg.Go(func() {
			rng := rand.New(rand.NewSource(seed + int64(r) + 1))
			for {
				select {
				case <-done:
					return
				default:
				}
				n := published.Load()
				if n == 0 {
					continue
				}
				k := keys[rng.Int63n(n)]
				got, ok := s.Get(k)
				if !assert.Truef(t, ok, "Get(%q): missing after writer published it", k) {
					return
				}
				if !assert.Equalf(t, string(k), string(got), "Get(%q): want key itself", k) {
					return
				}
			}
		})
	}

	// seek reader: Seek to a published key must land the cursor exactly on it
	wg.Go(func() {
		rng := rand.New(rand.NewSource(seed + numReaders + 1))
		it := s.NewIterator()
		for {
			select {
			case <-done:
				return
			default:
			}
			n := published.Load()
			if n == 0 {
				continue
			}
			k := keys[rng.Int63n(n)]
			it.Seek(k)
			if !assert.Truef(t, it.Valid() && bytes.Equal(it.Key(), k),
				"Seek(%q): cursor not on key after writer published it", k) {
				return
			}
		}
	})

	// scan reader: repeated full walks; order must stay strictly ascending
	// even while the writer splices
	wg.Go(func() {
		var prev []byte
		for {
			select {
			case <-done:
				return
			default:
			}
			it := s.NewIterator()
			prev = prev[:0]
			for it.SeekToFirst(); it.Valid(); it.Next() {
				k := it.Key()
				if len(prev) > 0 && !assert.Lessf(t, bytes.Compare(prev, k), 0,
					"concurrent walk not ascending: %q then %q", prev, k) {
					return
				}
				prev = append(prev[:0], k...)
			}
		}
	})

	wg.Wait()

	// final oracle: every key present, full walk strictly sorted, exact count
	for _, k := range keys {
		got, ok := s.Get(k)
		require.Truef(t, ok, "final Get(%q)", k)
		require.Equalf(t, string(k), string(got), "final Get(%q): want key itself", k)
	}
	it := s.NewIterator()
	count := 0
	var prev []byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if len(prev) > 0 {
			require.Lessf(t, bytes.Compare(prev, it.Key()), 0,
				"final walk not ascending at %d: %q then %q", count, prev, it.Key())
		}
		prev = append(prev[:0], it.Key()...)
		count++
	}
	require.Equal(t, numKeys, count, "final walk count")
}

// TestMillionInserts: 1M random-order inserts, full iteration strictly
// sorted with an exact positional oracle.
func TestMillionInserts(t *testing.T) {
	if testing.Short() {
		t.Skip("1M inserts, skipped in -short")
	}
	const n = 1_000_000

	rng := rand.New(rand.NewSource(seed))
	s := newList()
	for _, p := range rng.Perm(n) {
		k := fmt.Appendf(nil, "key%08d", p)
		s.Insert(k, k)
	}

	// zero-padded keys sort lexicographically == numerically, so position i
	// in the walk must be exactly key%08d of i
	it := s.NewIterator()
	i := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		want := fmt.Appendf(nil, "key%08d", i)
		require.Equalf(t, string(want), string(it.Key()), "walk[%d] key", i)
		i++
	}
	require.Equal(t, n, i, "walk entry count")

	// spot-check Gets across the range
	for j := 0; j < n; j += 997 {
		k := fmt.Appendf(nil, "key%08d", j)
		got, ok := s.Get(k)
		require.Truef(t, ok, "Get(%q)", k)
		require.Equalf(t, string(k), string(got), "Get(%q): want key itself", k)
	}
}
