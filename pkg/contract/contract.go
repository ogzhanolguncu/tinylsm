//go:build !nocontract

// Package contract provides executable preconditions, postconditions, and
// invariants. Build with -tags nocontract to compile them out.
//
// Stripping removes the call, not its arguments — a bare
// Require(expensiveCheck(x), ...) still runs expensiveCheck. Guard those:
//
//	if contract.Enabled {
//	    contract.Require(expensiveCheck(x), "...")
//	}
package contract

import "fmt"

// Enabled is a constant, so the guard above is dead-code eliminated.
const Enabled = true

func Require(cond bool, msg string, args ...any) {
	check(cond, "precondition", msg, args...)
}

func Ensure(cond bool, msg string, args ...any) {
	check(cond, "postcondition", msg, args...)
}

func Invariant(cond bool, msg string, args ...any) {
	check(cond, "invariant", msg, args...)
}

func Assert(cond bool, msg string, args ...any) {
	check(cond, "assertion", msg, args...)
}

func check(cond bool, kind, msg string, args ...any) {
	if !cond {
		panic(fmt.Sprintf("%s violated: %s", kind, fmt.Sprintf(msg, args...)))
	}
}
