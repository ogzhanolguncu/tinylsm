package keys

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func corpus(tb testing.TB) [][]byte {
	tb.Helper()
	userKeys := []string{"a", "ab", "abc", "b", "foo", "goo", "\x00", "\x00\x00", "\xff"}
	seqs := []uint64{0, 1, 2, 9, 1 << 30, MaxSeq}

	var out [][]byte
	for _, uk := range userKeys {
		for _, seq := range seqs {
			out = append(out, mustEncode(tb, uk, seq))
		}
	}
	return out
}

// The sign says which key sorts FIRST, not which is numerically larger.
func TestCompareOrdering(t *testing.T) {
	cases := []struct {
		name string
		a, b []byte
		want int
	}{
		{"same user key, higher seq sorts first", mustEncode(t, "foo", 9), mustEncode(t, "foo", 5), -1},
		{"same user key, lower seq sorts last", mustEncode(t, "foo", 5), mustEncode(t, "foo", 9), 1},
		{"identical keys", mustEncode(t, "foo", 9), mustEncode(t, "foo", 9), 0},
		{"user key decides, seq ignored", mustEncode(t, "foo", 9), mustEncode(t, "goo", 1), -1},
		{"user key decides, reversed", mustEncode(t, "goo", 1), mustEncode(t, "foo", 9), 1},
		{"seq 0 vs MaxSeq", mustEncode(t, "k", MaxSeq), mustEncode(t, "k", 0), -1},
		{"binary user key, higher seq first", mustEncode(t, "\x00", 2), mustEncode(t, "\x00", 1), -1},

		// Returning 0 for two different byte slices is safe only because seq is
		// unique per write, so this pair can never reach the comparator.
		{"same user key and seq, kind ignored", mustEncodeKind(t, "foo", 5, KindPut), mustEncodeKind(t, "foo", 5, KindDelete), 0},
		{"kind never outranks seq", mustEncodeKind(t, "foo", 9, KindPut), mustEncodeKind(t, "foo", 5, KindDelete), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sign(Compare(tc.a, tc.b)))
		})
	}
}

// Comparing whole internal keys byte-for-byte lines the shorter key's TRAILER
// up against the longer key's USER KEY. Only the user-key bytes may decide.
func TestComparePrefixKeysDoNotBleedIntoTrailer(t *testing.T) {
	cases := []struct {
		name string
		a, b []byte
		want int
	}{
		{`"ab" before "abc", equal seq`, mustEncode(t, "ab", 1), mustEncode(t, "abc", 1), -1},
		{`"abc" after "ab", equal seq`, mustEncode(t, "abc", 1), mustEncode(t, "ab", 1), 1},
		{`"ab"@MaxSeq still before "abc"@0`, mustEncode(t, "ab", MaxSeq), mustEncode(t, "abc", 0), -1},
		{`"abc"@0 still after "ab"@MaxSeq`, mustEncode(t, "abc", 0), mustEncode(t, "ab", MaxSeq), 1},
		{`"a" before "ab"`, mustEncode(t, "a", 0), mustEncode(t, "ab", MaxSeq), -1},
		{`0x00 user key is not a terminator`, mustEncode(t, "\x00", 1), mustEncode(t, "\x00\x00", 1), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sign(Compare(tc.a, tc.b)))
		})
	}
}

func TestCompareProducesExpectedTotalOrder(t *testing.T) {
	userKeys := []string{"a", "ab", "abc", "b"}
	seqs := []uint64{1, 2, 9}

	var keys [][]byte
	for _, uk := range userKeys {
		for _, seq := range seqs {
			keys = append(keys, mustEncode(t, uk, seq))
		}
	}
	rand.New(rand.NewSource(7)).Shuffle(len(keys), func(i, j int) {
		keys[i], keys[j] = keys[j], keys[i]
	})

	sort.Slice(keys, func(i, j int) bool { return Compare(keys[i], keys[j]) < 0 })

	var got []string
	for _, ik := range keys {
		uk, seq, _, err := Decode(ik)
		require.NoError(t, err)
		got = append(got, fmt.Sprintf("%s@%d", uk, seq))
	}
	require.Equal(t, []string{
		"a@9", "a@2", "a@1",
		"ab@9", "ab@2", "ab@1",
		"abc@9", "abc@2", "abc@1",
		"b@9", "b@2", "b@1",
	}, got)
}

// The reason seq sorts descending. Get builds a lookup key (userKey, MaxSeq) and
// asks for the first entry >= it; descending seq is what makes that first entry
// the newest version rather than a miss past the end of the user key's run.
func TestCompareSeekLandsOnNewestVersion(t *testing.T) {
	keys := [][]byte{
		mustEncode(t, "aaa", 1),
		mustEncode(t, "foo", 5),
		mustEncode(t, "foo", 7),
		mustEncode(t, "foo", 9),
		mustEncode(t, "goo", 2),
	}
	sort.Slice(keys, func(i, j int) bool { return Compare(keys[i], keys[j]) < 0 })

	target := mustEncode(t, "foo", MaxSeq)
	i := sort.Search(len(keys), func(i int) bool { return Compare(keys[i], target) >= 0 })
	require.Less(t, i, len(keys), "seek must not run off the end")

	uk, seq, _, err := Decode(keys[i])
	require.NoError(t, err)
	require.Equal(t, "foo", string(uk))
	require.Equal(t, uint64(9), seq, "seek must land on the newest version of foo")
}

// Insurance for future rewrites of Compare. The x == y pairs subsume
// reflexivity: -n == n forces n to be zero.
func TestCompareIsAntisymmetric(t *testing.T) {
	all := corpus(t)
	for _, x := range all {
		for _, y := range all {
			forward, reverse := sign(Compare(x, y)), sign(Compare(y, x))
			require.Equalf(t, -forward, reverse, "Compare(%q, %q)=%d but Compare(%q, %q)=%d", x, y, forward, y, x, reverse)
		}
	}
}

func mustEncode(tb testing.TB, userKey string, seq uint64) []byte {
	tb.Helper()
	return mustEncodeKind(tb, userKey, seq, KindPut)
}

func mustEncodeKind(tb testing.TB, userKey string, seq uint64, kind Kind) []byte {
	tb.Helper()
	ik, err := Encode([]byte(userKey), seq, kind)
	require.NoError(tb, err)
	return ik
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
