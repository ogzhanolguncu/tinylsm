//go:build !nocontract

// Panic-asserting tests. Excluded under -tags nocontract, where the contracts
// they assert do not exist.
package keys

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Compare has no error return, so a malformed key must name the offending
// argument rather than die on a slice-bounds panic.
func TestComparePanicsOnShortKey(t *testing.T) {
	valid := mustEncode(t, "foo", 1)

	for n := range TrailerSize {
		short := make([]byte, n)

		msg := panicMessage(t, func() { Compare(short, valid) })
		require.Containsf(t, msg, "precondition violated", "len %d as a", n)
		require.Containsf(t, msg, fmt.Sprintf("a is %d bytes", n), "len %d as a", n)

		msg = panicMessage(t, func() { Compare(valid, short) })
		require.Containsf(t, msg, fmt.Sprintf("b is %d bytes", n), "len %d as b", n)
	}
}

func panicMessage(t *testing.T, f func()) string {
	t.Helper()

	var msg string
	func() {
		defer func() {
			r := recover()
			require.NotNil(t, r, "expected a panic")
			msg = fmt.Sprint(r)
		}()
		f()
	}()
	return msg
}
