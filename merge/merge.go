// Package merge turns N sorted iterators into one sorted stream.
package merge

import "github.com/ogzhanolguncu/tinylsm/keys"

type Iterator interface {
	SeekToFirst()
	Seek(target []byte)
	Valid() bool
	Next()
	Key() []byte
	Value() []byte
}

type Merger struct {
	children []Iterator
	current  Iterator
}

func New(children []Iterator) *Merger {
	return &Merger{
		children: children,
	}
}

func (m *Merger) findSmallest() {
	var smallest Iterator
	for _, c := range m.children {
		if !c.Valid() {
			continue
		}
		if smallest == nil {
			smallest = c
		} else {
			r := keys.Compare(c.Key(), smallest.Key())
			if r < 0 {
				smallest = c
			}
		}
	}
	m.current = smallest
}

func (m *Merger) SeekToFirst() {
	for _, c := range m.children {
		c.SeekToFirst()
	}
	m.findSmallest()
}
func (m *Merger) Seek(target []byte) { m.current.Seek(target) }
func (m *Merger) Valid() bool {
	if m.current == nil {
		return false
	}
	return m.current.Valid()
}
func (m *Merger) Next()         { m.current.Next(); m.findSmallest() }
func (m *Merger) Key() []byte   { return m.current.Key() }
func (m *Merger) Value() []byte { return m.current.Value() }

// compile-time check: a Merger is itself an Iterator, so mergers can nest.
var _ Iterator = (*Merger)(nil)
