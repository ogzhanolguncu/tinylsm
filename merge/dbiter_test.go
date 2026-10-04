package merge

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

func drainLive(d *DBIter) []string {
	var out []string
	for d.SeekToFirst(); d.Valid(); d.Next() {
		out = append(out, fmt.Sprintf("%s=%s", d.Key(), d.Value()))
	}
	return out
}

// The exact data from the REPL demo.
func TestDBIterREPLExample(t *testing.T) {
	mem := &sliceIter{es: []entry{
		{ik(t, "apple", 305, keys.KindPut), []byte("gold")},
		{ik(t, "banana", 304, keys.KindDelete), nil},
		{ik(t, "cherry", 303, keys.KindPut), []byte("dark")},
	}}
	sst := &sliceIter{es: []entry{
		{ik(t, "apple", 2, keys.KindPut), []byte("green")},
		{ik(t, "apple", 0, keys.KindPut), []byte("red")},
		{ik(t, "banana", 1, keys.KindPut), []byte("yellow")},
	}}
	d := NewDBIter(New([]Iterator{mem, sst}), keys.MaxSeq)
	want := []string{"apple=gold", "cherry=dark"}
	require.Equal(t, want, drainLive(d))
	require.Equal(t, want, drainLive(d), "SeekToFirst must rewind")
}

func TestDBIterTombstoneEdges(t *testing.T) {
	cases := map[string]struct {
		es   []entry
		want []string
	}{
		"empty":              {nil, nil},
		"only a tombstone":   {[]entry{{ik(t, "a", 1, keys.KindDelete), nil}}, nil},
		"first key deleted":  {[]entry{{ik(t, "a", 2, keys.KindDelete), nil}, {ik(t, "a", 1, keys.KindPut), []byte("x")}, {ik(t, "b", 3, keys.KindPut), []byte("y")}}, []string{"b=y"}},
		"last key deleted":   {[]entry{{ik(t, "a", 1, keys.KindPut), []byte("x")}, {ik(t, "b", 3, keys.KindDelete), nil}, {ik(t, "b", 2, keys.KindPut), []byte("y")}}, []string{"a=x"}},
		"deletes in a row":   {[]entry{{ik(t, "a", 1, keys.KindDelete), nil}, {ik(t, "b", 2, keys.KindDelete), nil}, {ik(t, "c", 3, keys.KindDelete), nil}, {ik(t, "d", 4, keys.KindPut), []byte("z")}}, []string{"d=z"}},
		"resurrected":        {[]entry{{ik(t, "a", 3, keys.KindPut), []byte("back")}, {ik(t, "a", 2, keys.KindDelete), nil}, {ik(t, "a", 1, keys.KindPut), []byte("old")}}, []string{"a=back"}},
		"put then delete":    {[]entry{{ik(t, "a", 2, keys.KindDelete), nil}, {ik(t, "a", 1, keys.KindPut), []byte("old")}}, nil},
		"empty value is set": {[]entry{{ik(t, "a", 1, keys.KindPut), []byte{}}}, []string{"a="}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.want, drainLive(NewDBIter(New([]Iterator{&sliceIter{es: c.es}}), keys.MaxSeq)))
		})
	}
}

// Property: replay every write in seq order into a map — that's what the DB
// "means". The iterator must show exactly that map, sorted.
func TestDBIterMatchesOracle(t *testing.T) {
	for round := range 300 {
		rng := rand.New(rand.NewPCG(uint64(round), 1))
		seq := uint64(1)
		live := map[string]string{}
		var children []Iterator
		for range rng.IntN(8) {
			var es []entry
			for range rng.IntN(30) {
				user := fmt.Sprintf("k%02d", rng.IntN(15))
				if rng.IntN(4) == 0 {
					es = append(es, entry{ik(t, user, seq, keys.KindDelete), nil})
					delete(live, user)
				} else {
					v := fmt.Sprintf("v%d", seq)
					es = append(es, entry{ik(t, user, seq, keys.KindPut), []byte(v)})
					live[user] = v
				}
				seq++
			}
			slices.SortFunc(es, func(a, b entry) int { return keys.Compare(a.k, b.k) })
			children = append(children, &sliceIter{es: es})
		}

		var want []string
		for _, k := range slices.Sorted(func(yield func(string) bool) {
			for k := range live {
				if !yield(k) {
					return
				}
			}
		}) {
			want = append(want, k+"="+live[k])
		}
		require.Equal(t, want, drainLive(NewDBIter(New(children), keys.MaxSeq)), "round %d", round)
	}
}

// A corrupt key must stop the scan AND say so; otherwise it looks like the end of data.
func TestDBIterCorruptKeyReportsError(t *testing.T) {
	badKind := ik(t, "b", 2, keys.KindPut)
	badKind[len(badKind)-8] = 0x7f // low trailer byte is the kind
	src := &sliceIter{es: []entry{
		{ik(t, "a", 1, keys.KindPut), []byte("x")},
		{badKind, []byte("y")},
	}}
	d := NewDBIter(src, keys.MaxSeq) // no Merger: its compare would trip on the same bytes
	require.Equal(t, []string{"a=x"}, drainLive(d))
	require.ErrorIs(t, d.Error(), keys.ErrUnknownKind)

	src.es = src.es[:1]
	drainLive(d)
	require.NoError(t, d.Error(), "SeekToFirst starts clean")
}

// The scan started when the next seq was 10. Anything at seq >= 10 happened
// later and must be invisible, including later deletes.
func TestDBIterSnapshot(t *testing.T) {
	src := &sliceIter{es: []entry{
		{ik(t, "alice", 11, keys.KindPut), []byte("70")}, // after: hidden
		{ik(t, "alice", 7, keys.KindPut), []byte("100")},
		{ik(t, "bob", 12, keys.KindPut), []byte("80")}, // after: hidden
		{ik(t, "bob", 8, keys.KindPut), []byte("50")},
		{ik(t, "carol", 13, keys.KindDelete), nil}, // deleted after: still visible
		{ik(t, "carol", 9, keys.KindPut), []byte("5")},
		{ik(t, "dave", 10, keys.KindPut), []byte("1")}, // created after: invisible
		{ik(t, "erin", 4, keys.KindDelete), nil},       // deleted before: gone
		{ik(t, "erin", 3, keys.KindPut), []byte("9")},
	}}
	d := NewDBIter(New([]Iterator{src}), 10)
	require.Equal(t, []string{"alice=100", "bob=50", "carol=5"}, drainLive(d))
	require.Equal(t, []string{"bob=50", "carol=5"}, liveFrom(d, "b"))
	require.Equal(t, []string{"alice=70", "bob=80", "dave=1"}, drainLive(NewDBIter(New([]Iterator{src}), keys.MaxSeq)))
}

func TestDBIterInternalKeyKeepsOriginalSeq(t *testing.T) {
	d := replData(t)
	var got []string
	for d.SeekToFirst(); d.Valid(); d.Next() {
		got = append(got, show(t, []entry{{d.InternalKey(), d.Value()}})...)
	}
	require.Equal(t, []string{"apple@305:put=gold", "cherry@303:put=dark", "date@3:put=brown"}, got)
}
