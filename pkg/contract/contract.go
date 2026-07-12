package contract

import "fmt"

// Enabled lets you compile contracts out in production builds.
var Enabled = true

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
	if Enabled && !cond {
		panic(fmt.Sprintf("%s violated: %s", kind, fmt.Sprintf(msg, args...)))
	}
}
