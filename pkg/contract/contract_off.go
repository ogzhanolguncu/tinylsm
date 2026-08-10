//go:build nocontract

package contract

// Enabled is a constant false in this build, so `if contract.Enabled { ... }`
// blocks are removed entirely by dead-code elimination.
const Enabled = false

func Require(cond bool, msg string, args ...any)   {}
func Ensure(cond bool, msg string, args ...any)    {}
func Invariant(cond bool, msg string, args ...any) {}
func Assert(cond bool, msg string, args ...any)    {}
