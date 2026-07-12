package main

import (
	"fmt"
	"math"
	"strings"
)

// dump prints the skiplist level by level, top to bottom. Debug helper only.
func (s *SkipList) dump() {
	for lvl := int(s.height) - 1; lvl >= 0; lvl-- {
		var b strings.Builder
		fmt.Fprintf(&b, "L%d: head", lvl)
		for n := s.head.fp[lvl]; n != nil; n = n.fp[lvl] {
			fmt.Fprintf(&b, " -> %s", n.key)
		}
		fmt.Println(b.String())
	}
	fmt.Println()
}

// towers renders each node as a vertical bar of its tower height, in key order.
// Level 0 at the bottom. You see express lanes thin out toward the top.
func (s *SkipList) towers() {
	// collect nodes in order (walk level 0)
	var nodes []*Node
	for n := s.head.fp[0]; n != nil; n = n.fp[0] {
		nodes = append(nodes, n)
	}
	for lvl := int(s.height) - 1; lvl >= 0; lvl-- {
		var b strings.Builder
		fmt.Fprintf(&b, "L%-2d ", lvl)
		for _, n := range nodes {
			if len(n.fp) > lvl {
				b.WriteString(" █ ")
			} else {
				b.WriteString("   ")
			}
		}
		fmt.Println(b.String())
	}
	// key labels
	var b strings.Builder
	b.WriteString("    ")
	for _, n := range nodes {
		fmt.Fprintf(&b, "%2s ", n.key)
	}
	fmt.Println(b.String())
	fmt.Println()
}

// histogram prints the count of nodes at each tower height vs the theoretical
// expectation for a p=0.25 geometric distribution.
func (s *SkipList) histogram() {
	counts := make([]int, kMaxHeight+1)
	total := 0
	for n := s.head.fp[0]; n != nil; n = n.fp[0] {
		counts[len(n.fp)]++
		total++
	}
	fmt.Printf("height  actual  expected  (n=%d)\n", total)
	for h := 1; h <= int(s.height); h++ {
		// P(height == h) = p^(h-1) * (1-p)   [capped level ignored for display]
		prob := math.Pow(p, float64(h-1)) * (1 - p)
		fmt.Printf("  %-4d  %-6d  %.1f\n", h, counts[h], prob*float64(total))
	}
	fmt.Println()
}
