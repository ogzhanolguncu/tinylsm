package main

import (
	"math/rand"
)

const (
	kMaxHeight = 12
	p          = 0.25
)

type Node struct {
	key []byte
	val []byte
	fp  []*Node
}

type Comparator func(a, b []byte) int

type (
	SkipList struct {
		comparator Comparator
		rng        *rand.Rand
		head       *Node
		height     uint8
	}
)

func New(cmp Comparator, seed int64) *SkipList {
	return &SkipList{
		height: 1,
		head: &Node{
			fp: make([]*Node, kMaxHeight),
		},
		rng:        rand.New(rand.NewSource(seed)),
		comparator: cmp,
	}
}

func (s *SkipList) randomHeight() uint8 {
	h := uint8(1)
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
	cur := s.head
	update := make([]*Node, kMaxHeight)
	for lvl := int(s.height) - 1; lvl >= 0; lvl-- { // Going down
		for cur.fp[lvl] != nil && s.comparator(cur.fp[lvl].key, key) < 0 { // Going right
			cur = cur.fp[lvl]
		}
		update[lvl] = cur
	}

	h := s.randomHeight()
	if h > s.height {
		for i := s.height; i < h; i++ {
			update[i] = s.head
		}
		s.height = h
	}

	node := &Node{key: key, val: val, fp: make([]*Node, h)}

	for i := 0; i < int(h); i++ {
		node.fp[i] = update[i].fp[i] // New node points to old nodes next position
		update[i].fp[i] = node       // Old node points to new node
	}
}
