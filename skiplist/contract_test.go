//go:build !nocontract

// Panic-asserting tests. Excluded under -tags nocontract, where the contracts
// they assert do not exist.
package skiplist

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewRejectsNilComparator(t *testing.T) {
	require.Panics(t, func() { New(nil, seed) })
}

// Asserts the message, not just that something panicked: a bare require.Panics
// passes on the nil deref these methods did before the contracts existed.
func TestIteratorMethodsRequireValid(t *testing.T) {
	cases := map[string]func(*SkipListIterator){
		"Key":   func(it *SkipListIterator) { _ = it.Key() },
		"Value": func(it *SkipListIterator) { _ = it.Value() },
		"Next":  func(it *SkipListIterator) { it.Next() },
	}

	for name, call := range cases {
		want := "precondition violated: SkipListIterator." + name

		t.Run("empty list/"+name, func(t *testing.T) {
			it := newList().NewIterator()
			it.SeekToFirst()
			require.False(t, it.Valid())
			require.Contains(t, panicMessage(t, func() { call(it) }), want)
		})

		t.Run("walked off the end/"+name, func(t *testing.T) {
			s := newList()
			s.Insert([]byte("k"), []byte("v"))
			it := s.NewIterator()
			for it.SeekToFirst(); it.Valid(); it.Next() {
			}
			require.Contains(t, panicMessage(t, func() { call(it) }), want)
		})
	}
}

// Replaces an earlier TestOverwrite asserting second-Insert-wins. That worked
// only incidentally and no LSM caller relies on it.
func TestDuplicateInsertPanics(t *testing.T) {
	s := newList()
	s.Insert([]byte("k"), []byte("v1"))
	msg := panicMessage(t, func() { s.Insert([]byte("k"), []byte("v2")) })
	require.Contains(t, msg, "skiplist.Insert: duplicate key")
}

func panicMessage(t *testing.T, fn func()) string {
	t.Helper()

	var msg string
	func() {
		defer func() {
			r := recover()
			require.NotNil(t, r, "expected a panic")
			msg = fmt.Sprint(r)
		}()
		fn()
	}()
	return msg
}
