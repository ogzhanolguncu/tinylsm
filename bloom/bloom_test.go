package bloom

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func keySet(prefix string, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = fmt.Appendf(nil, "%s%08d", prefix, i)
	}
	return out
}

// The one promise a bloom filter makes: an added key is never reported absent.
// Breaking it makes Get skip a table that holds the key.
func TestNoFalseNegatives(t *testing.T) {
	for _, n := range []int{0, 1, 7, 100, 10_000} {
		ks := keySet("in:", n)
		f := Build(ks, BitsPerKey)
		for _, k := range ks {
			require.True(t, MayContain(f, k), "n=%d: %s added but reported absent", n, k)
		}
	}
}

// ~1% in theory at 10 bits/key; allow 2× before calling it broken.
func TestFalsePositiveRate(t *testing.T) {
	f := Build(keySet("in:", 10_000), BitsPerKey)
	fp := 0
	const probes = 100_000
	for _, k := range keySet("out:", probes) {
		if MayContain(f, k) {
			fp++
		}
	}
	rate := float64(fp) / probes
	t.Logf("false positive rate: %.2f%%", rate*100)
	require.Less(t, rate, 0.02)
}

func TestEmptyFilterRulesOutEverything(t *testing.T) {
	f := Build(nil, BitsPerKey)
	require.False(t, MayContain(f, []byte("anything")))
}

// A filter we can't read must answer "maybe": "no" would hide real data.
func TestUnusableFilterSaysMaybe(t *testing.T) {
	require.True(t, MayContain(nil, []byte("k")))
	require.True(t, MayContain([]byte{0xff}, []byte("k")))
	require.True(t, MayContain([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte("k")), "k=0 is never written")
}
