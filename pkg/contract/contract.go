//go:build !nocontract

// Package contract provides executable preconditions, postconditions, and
// invariants.
//
// Contracts are on by default. Build with `-tags nocontract` to compile them
// out: Enabled becomes an untyped constant false, the check functions become
// empty, and the compiler eliminates both the calls and any `if
// contract.Enabled { ... }` block wrapped around them.
//
// Note the limit of that elimination: arguments to a call are still evaluated.
//
//	contract.Require(expensiveCheck(x), "...")   // expensiveCheck STILL runs
//
// For an assertion whose condition costs real work on a hot path, guard it so
// the condition itself is inside the dead branch:
//
//	if contract.Enabled {
//	    contract.Require(expensiveCheck(x), "...")
//	}
package contract

import "fmt"

// Enabled reports whether contracts are compiled in. It is a constant, so it
// is usable as a compile-time switch.
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
