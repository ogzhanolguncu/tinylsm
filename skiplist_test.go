package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

const seed = 42

func newList() *SkipList {
	return New(bytes.Compare, seed)
}

func TestInsertGet(t *testing.T) {
	s := newList()
	s.Insert([]byte("foo"), []byte("bar"))

	got, ok := s.Get([]byte("foo"))
	if !ok {
		t.Fatal("Get(foo): ok=false, want true")
	}
	if !bytes.Equal(got, []byte("bar")) {
		t.Fatalf("Get(foo)=%q, want %q", got, "bar")
	}
}

func TestGetMissing(t *testing.T) {
	s := newList()
	s.Insert([]byte("foo"), []byte("bar"))

	if _, ok := s.Get([]byte("nope")); ok {
		t.Fatal("Get(nope): ok=true, want false")
	}
}

func TestGetEmpty(t *testing.T) {
	s := newList()
	if _, ok := s.Get([]byte("foo")); ok {
		t.Fatal("Get on empty list: ok=true, want false")
	}
}

func TestOverwrite(t *testing.T) {
	s := newList()
	s.Insert([]byte("k"), []byte("v1"))
	s.Insert([]byte("k"), []byte("v2"))

	got, ok := s.Get([]byte("k"))
	if !ok {
		t.Fatal("Get(k): ok=false, want true")
	}
	if !bytes.Equal(got, []byte("v2")) {
		t.Fatalf("Get(k)=%q, want latest %q", got, "v2")
	}
}

func TestDelete(t *testing.T) {
	s := newList()
	s.Insert([]byte("a"), []byte("1"))
	s.Insert([]byte("b"), []byte("2"))

	if !s.Delete([]byte("a")) {
		t.Fatal("Delete(a): false, want true")
	}
	if _, ok := s.Get([]byte("a")); ok {
		t.Fatal("Get(a) after delete: ok=true, want false")
	}
	// sibling untouched
	if _, ok := s.Get([]byte("b")); !ok {
		t.Fatal("Get(b) after deleting a: ok=false, want true")
	}
}

func TestDeleteMissing(t *testing.T) {
	s := newList()
	s.Insert([]byte("a"), []byte("1"))

	if s.Delete([]byte("zzz")) {
		t.Fatal("Delete(zzz): true, want false")
	}
	// list intact
	if _, ok := s.Get([]byte("a")); !ok {
		t.Fatal("Get(a) after failed delete: ok=false, want true")
	}
}

func TestDeleteEmpty(t *testing.T) {
	s := newList()
	if s.Delete([]byte("a")) {
		t.Fatal("Delete on empty list: true, want false")
	}
}

// walkKeys returns level-0 keys in order (same-package access to internals).
func walkKeys(s *SkipList) [][]byte {
	var out [][]byte
	for n := s.head.fp[0]; n != nil; n = n.fp[0] {
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
		if bytes.Compare(keys[i-1], keys[i]) >= 0 {
			t.Fatalf("not sorted at %d: %q then %q", i, keys[i-1], keys[i])
		}
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
		if !ok || !bytes.Equal(got, k) {
			t.Fatalf("Get(%q)=%q ok=%v, want %q true", k, got, ok, k)
		}
	}
	if len(walkKeys(s)) != n {
		t.Fatalf("level-0 count=%d, want %d", len(walkKeys(s)), n)
	}
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
		if i >= len(sorted) {
			t.Fatalf("iterator yielded more than %d entries", len(sorted))
		}
		if prev != nil && bytes.Compare(prev, it.Key()) >= 0 {
			t.Fatalf("not strictly ascending at %d: %q then %q", i, prev, it.Key())
		}
		if want := sorted[i]; string(it.Key()) != want {
			t.Fatalf("walk[%d] key=%q, want %q", i, it.Key(), want)
		}
		if want := oracle[sorted[i]]; !bytes.Equal(it.Value(), want) {
			t.Fatalf("walk[%d] val=%q, want %q", i, it.Value(), want)
		}
		prev = append(prev[:0], it.Key()...)
		i++
	}
	if i != len(sorted) {
		t.Fatalf("walk yielded %d entries, want %d", i, len(sorted))
	}

	// 2. exact-match Seek lands on the key, value matches.
	for k, v := range oracle {
		it.Seek([]byte(k))
		if !it.Valid() {
			t.Fatalf("Seek(%q): !Valid, want hit", k)
		}
		if string(it.Key()) != k {
			t.Fatalf("Seek(%q) landed on %q", k, it.Key())
		}
		if !bytes.Equal(it.Value(), v) {
			t.Fatalf("Seek(%q) val=%q, want %q", k, it.Value(), v)
		}
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
			if it.Valid() {
				t.Fatalf("Seek(%q): Valid=%q, want past-end", probe, it.Key())
			}
			continue
		}
		if !it.Valid() {
			t.Fatalf("Seek(%q): !Valid, want %q", probe, sorted[idx])
		}
		if string(it.Key()) != sorted[idx] {
			t.Fatalf("Seek(%q) landed on %q, want %q", probe, it.Key(), sorted[idx])
		}
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
