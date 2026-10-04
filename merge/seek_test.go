package merge

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

func drainFrom(m *Merger, target []byte) []entry {
	var out []entry
	for m.Seek(target); m.Valid(); m.Next() {
		out = append(out, entry{m.Key(), m.Value()})
	}
	return out
}

func liveFrom(d *DBIter, userKey string) []string {
	var out []string
	for d.Seek([]byte(userKey)); d.Valid(); d.Next() {
		out = append(out, fmt.Sprintf("%s=%s", d.Key(), d.Value()))
	}
	return out
}

// Property: merger.Seek(t) yields exactly the sorted entries >= t.
func TestMergerSeekMatchesOracle(t *testing.T) {
	for round := range 300 {
		rng := rand.New(rand.NewPCG(uint64(round), 2))
		seq := uint64(1)
		var children []Iterator
		var all []entry
		for range rng.IntN(6) {
			var es []entry
			for range rng.IntN(20) {
				es = append(es, entry{ik(t, fmt.Sprintf("k%02d", rng.IntN(15)), seq, keys.KindPut), fmt.Appendf(nil, "v%d", seq)})
				seq++
			}
			slices.SortFunc(es, func(a, b entry) int { return keys.Compare(a.k, b.k) })
			children = append(children, &sliceIter{es: es})
			all = append(all, es...)
		}
		slices.SortFunc(all, func(a, b entry) int { return keys.Compare(a.k, b.k) })

		target := ik(t, fmt.Sprintf("k%02d", rng.IntN(17)), uint64(rng.IntN(int(seq)+1)), keys.KindPut)
		i, _ := slices.BinarySearchFunc(all, target, func(e entry, t []byte) int { return keys.Compare(e.k, t) })

		m := New(children)
		require.Equal(t, show(t, all[i:]), show(t, drainFrom(m, target)), "round %d", round)
		// seeking again after draining must still work
		require.Equal(t, show(t, all[i:]), show(t, drainFrom(m, target)), "round %d reseek", round)
	}
}

// The REPL data again: apple(3 versions), banana(deleted), cherry.
func replData(t *testing.T) *DBIter {
	mem := &sliceIter{es: []entry{
		{ik(t, "apple", 305, keys.KindPut), []byte("gold")},
		{ik(t, "banana", 304, keys.KindDelete), nil},
		{ik(t, "cherry", 303, keys.KindPut), []byte("dark")},
	}}
	sst := &sliceIter{es: []entry{
		{ik(t, "apple", 2, keys.KindPut), []byte("green")},
		{ik(t, "apple", 0, keys.KindPut), []byte("red")},
		{ik(t, "banana", 1, keys.KindPut), []byte("yellow")},
		{ik(t, "date", 3, keys.KindPut), []byte("brown")},
	}}
	return NewDBIter(New([]Iterator{mem, sst}), keys.MaxSeq)
}

func TestDBIterSeek(t *testing.T) {
	cases := []struct {
		seek string
		want []string
	}{
		{"", []string{"apple=gold", "cherry=dark", "date=brown"}},
		{"apple", []string{"apple=gold", "cherry=dark", "date=brown"}}, // newest apple, not an old one
		{"apricot", []string{"cherry=dark", "date=brown"}},             // between keys: next one up
		{"banana", []string{"cherry=dark", "date=brown"}},              // deleted key: skip to next live
		{"date", []string{"date=brown"}},
		{"zzz", nil}, // past the end
	}
	d := replData(t)
	for _, c := range cases {
		require.Equal(t, c.want, liveFrom(d, c.seek), "seek %q", c.seek)
	}
}
