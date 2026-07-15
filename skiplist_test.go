package main

import (
	"bytes"
	"fmt"
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
