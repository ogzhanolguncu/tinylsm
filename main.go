package main

import (
	"bytes"

	"github.com/ogzhanolguncu/tinylsm/skiplist"
)

func main() {
	cmp := func(a, b []byte) int { return bytes.Compare(a, b) }
	s := skiplist.New(cmp, 42)
	for _, k := range []string{"5", "2", "8", "1", "9", "3"} {
		s.Insert([]byte(k), []byte("v"+k))
	}
}
