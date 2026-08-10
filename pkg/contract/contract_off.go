//go:build nocontract

package contract

const Enabled = false

func Require(cond bool, msg string, args ...any)   {}
func Ensure(cond bool, msg string, args ...any)    {}
func Invariant(cond bool, msg string, args ...any) {}
func Assert(cond bool, msg string, args ...any)    {}
