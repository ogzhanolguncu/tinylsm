package main

import "bytes"

func main() {
	cmp := func(a, b []byte) int { return bytes.Compare(a, b) }
	s := New(cmp, 42)
	for _, k := range []string{"5", "2", "8", "1", "9", "3"} {
		s.Insert([]byte(k), []byte("v"+k))
	}
	s.dump()
}
