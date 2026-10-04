package merge

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/skiplist"
	"github.com/ogzhanolguncu/tinylsm/sstable"
	"github.com/stretchr/testify/require"
)

// The real iterators must fit the interface, or the DB can't feed them in.
var (
	_ Iterator = (*sstable.Iter)(nil)
	_ Iterator = (*skiplist.SkipListIterator)(nil)
)

type entry struct{ k, v []byte }

// sliceIter is a fake child: a sorted slice pretending to be a table.
type sliceIter struct {
	es []entry
	i  int
}

func (s *sliceIter) SeekToFirst() { s.i = 0 }
func (s *sliceIter) Seek(t []byte) {
	s.i, _ = slices.BinarySearchFunc(s.es, t, func(e entry, t []byte) int { return keys.Compare(e.k, t) })
}
func (s *sliceIter) Valid() bool   { return s.i < len(s.es) }
func (s *sliceIter) Next()         { s.i++ }
func (s *sliceIter) Key() []byte   { return s.es[s.i].k }
func (s *sliceIter) Value() []byte { return s.es[s.i].v }

func ik(t *testing.T, user string, seq uint64, kind keys.Kind) []byte {
	k, err := keys.Encode([]byte(user), seq, kind)
	require.NoError(t, err)
	return k
}

func drain(m *Merger) []entry {
	var out []entry
	for m.SeekToFirst(); m.Valid(); m.Next() {
		out = append(out, entry{m.Key(), m.Value()})
	}
	return out
}

func show(t *testing.T, es []entry) []string {
	var out []string
	for _, e := range es {
		u, seq, kind, err := keys.Decode(e.k)
		require.NoError(t, err)
		out = append(out, fmt.Sprintf("%s@%d:%s=%s", u, seq, kind, e.v))
	}
	return out
}

func TestMergeTwoTables(t *testing.T) {
	older := &sliceIter{es: []entry{
		{ik(t, "a", 1, keys.KindPut), []byte("a1")},
		{ik(t, "c", 2, keys.KindPut), []byte("c2")},
	}}
	newer := &sliceIter{es: []entry{
		{ik(t, "b", 3, keys.KindPut), []byte("b3")},
		{ik(t, "c", 4, keys.KindDelete), nil},
	}}
	m := New([]Iterator{older, newer})
	want := []string{"a@1:put=a1", "b@3:put=b3", "c@4:delete=", "c@2:put=c2"}
	require.Equal(t, want, show(t, drain(m)))
	require.Equal(t, want, show(t, drain(m)), "SeekToFirst must rewind an exhausted merger")
}

func TestMergeEdgeCases(t *testing.T) {
	require.Empty(t, drain(New(nil)), "no children")
	require.Empty(t, drain(New([]Iterator{&sliceIter{}, &sliceIter{}})), "all children empty")

	one := &sliceIter{es: []entry{{ik(t, "x", 1, keys.KindPut), []byte("x")}}}
	require.Equal(t, []string{"x@1:put=x"}, show(t, drain(New([]Iterator{&sliceIter{}, one, &sliceIter{}}))))
}

// Property: k random sorted children, merged == concat + sort.
func TestMergeMatchesOracle(t *testing.T) {
	for round := range 300 {
		rng := rand.New(rand.NewPCG(uint64(round), 0))
		seq := uint64(1)
		var children []Iterator
		var all []entry
		for range rng.IntN(8) {
			var es []entry
			for range rng.IntN(30) {
				// few distinct user keys, so versions collide across children
				user := fmt.Sprintf("k%02d", rng.IntN(15))
				kind := keys.KindPut
				if rng.IntN(5) == 0 {
					kind = keys.KindDelete
				}
				es = append(es, entry{ik(t, user, seq, kind), fmt.Appendf(nil, "v%d", seq)})
				seq++
			}
			slices.SortFunc(es, func(a, b entry) int { return keys.Compare(a.k, b.k) })
			children = append(children, &sliceIter{es: es})
			all = append(all, es...)
		}
		slices.SortFunc(all, func(a, b entry) int { return keys.Compare(a.k, b.k) })

		require.Equal(t, show(t, all), show(t, drain(New(children))), "round %d", round)
	}
}
