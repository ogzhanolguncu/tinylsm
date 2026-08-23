package skiplist

import (
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
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
	contract.Require(cmp != nil, "skiplist.New: comparator must not be nil")

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

	// seqnums make every insert unique, so a duplicate is a caller bug.
	first := update[0].next(0) // first node >= key, or nil
	contract.Require(first == nil || s.comparator(first.key, key) != 0,
		"skiplist.Insert: duplicate key %x", key)

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

	// The Require above rules out an equal neighbour. This is its negative
	// space: after splicing, level 0 must still be strictly ascending. A bad
	// comparator or a bad splice is silent otherwise, and every binary search
	// downstream inherits the disorder.
	// Nil neighbours are branched on, not folded into the condition: message
	// arguments are evaluated even when the condition holds, so next.key would
	// deref a nil successor at the tail of the list.
	if contract.Enabled {
		if prev := update[0]; prev != s.head {
			contract.Ensure(s.comparator(prev.key, node.key) < 0,
				"skiplist.Insert: %x does not sort after its predecessor %x", node.key, prev.key)
		}
		if next := node.next(0); next != nil {
			contract.Ensure(s.comparator(node.key, next.key) < 0,
				"skiplist.Insert: %x does not sort before its successor %x", node.key, next.key)
		}
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
	return &SkipListIterator{list: s}
}

func (it *SkipListIterator) SeekToFirst() { it.cursor = it.list.head.next(0) }
func (it *SkipListIterator) Valid() bool  { return it.cursor != nil }

// Key, Value, and Next require a Valid iterator; without the contract they
// nil-deref.
func (it *SkipListIterator) Key() []byte {
	contract.Require(it.Valid(), "SkipListIterator.Key: iterator is exhausted")
	return it.cursor.key
}

func (it *SkipListIterator) Value() []byte {
	contract.Require(it.Valid(), "SkipListIterator.Value: iterator is exhausted")
	return it.cursor.val
}

func (it *SkipListIterator) Next() {
	contract.Require(it.Valid(), "SkipListIterator.Next: iterator is exhausted")
	it.cursor = it.cursor.next(0)
}

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
