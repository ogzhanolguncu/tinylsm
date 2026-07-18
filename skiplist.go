package main

import (
	"math/rand"
	"sync"
	"sync/atomic"
)

const (
	kMaxHeight = 12
	p          = 0.25
)

type Node struct {
	key []byte
	val []byte
	fp  []atomic.Pointer[Node]
}

func (s *Node) next(lvl int32) *Node { return s.fp[lvl].Load() }

type (
	Comparator func(a, b []byte) int
	SkipList   struct {
		comparator Comparator
		rng        *rand.Rand
		head       *Node
		height     atomic.Int32
		mu         sync.Mutex
	}
)

func New(cmp Comparator, seed int64) *SkipList {
	sl := &SkipList{
		head: &Node{
			fp: make([]atomic.Pointer[Node], kMaxHeight),
		},
		rng:        rand.New(rand.NewSource(seed)),
		comparator: cmp,
	}
	sl.height.Store(1)
	return sl
}

func (s *SkipList) randomHeight() int32 {
	h := int32(1)
	for h != kMaxHeight {
		if s.rng.Float64() < p {
			h++
		} else {
			break
		}
	}
	return h
}

func (s *SkipList) Insert(key, val []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.head
	update := make([]*Node, kMaxHeight)
	for lvl := s.height.Load() - 1; lvl >= 0; lvl-- { // Going down
		next := cur.next(lvl)
		for next != nil && s.comparator(next.key, key) < 0 { // Going right
			cur = next
			next = cur.next(lvl)
		}
		update[lvl] = cur
	}

	h := s.randomHeight()
	oldH := s.height.Load()
	if h > oldH {
		for i := oldH; i < h; i++ {
			update[i] = s.head
		}
	}

	// copy key and val so caller can reuse their slices after Insert
	// without mutating the skiplist's keys or the values Get serves
	node := &Node{key: append([]byte(nil), key...), val: append([]byte(nil), val...), fp: make([]atomic.Pointer[Node], h)}
	for i := 0; i < int(h); i++ {
		node.fp[i].Store(update[i].fp[i].Load()) // New node points to old nodes next position
		update[i].fp[i].Store(node)              // Old node points to new node
	}

	if h > oldH {
		s.height.Store(h)
	}
}

func (s *SkipList) Get(key []byte) ([]byte, bool) {
	cur := s.head
	var next *Node
	for lvl := s.height.Load() - 1; lvl >= 0; lvl-- { // Going down
		next = cur.next(lvl)
		for next != nil && s.comparator(next.key, key) < 0 { // Going right
			cur = next
			next = cur.next(lvl)
		}
	}

	if next == nil || s.comparator(key, next.key) != 0 {
		return nil, false
	}
	return next.val, true
}

type SkipListIterator struct {
	list   *SkipList
	cursor *Node
}

func (s *SkipList) NewIterator() *SkipListIterator {
	return &SkipListIterator{
		list:   s,
		cursor: s.head,
	}
}

func (it *SkipListIterator) SeekToFirst()  { it.cursor = it.list.head.next(0) }
func (it *SkipListIterator) Valid() bool   { return it.cursor != nil }
func (it *SkipListIterator) Key() []byte   { return it.cursor.key }
func (it *SkipListIterator) Value() []byte { return it.cursor.val }
func (it *SkipListIterator) Next()         { it.cursor = it.cursor.next(0) }

func (it *SkipListIterator) Seek(key []byte) {
	cur := it.list.head
	var next *Node
	for lvl := it.list.height.Load() - 1; lvl >= 0; lvl-- { // Going down
		next = cur.next(lvl)
		for next != nil && it.list.comparator(next.key, key) < 0 { // Going right
			cur = next
			next = cur.next(lvl)
		}
	}

	it.cursor = next
}
