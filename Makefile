.PHONY: run test test-race cover fuzz bench bench-contention bench-save bench-cmp

# filter benches: make bench BENCH=GetParallel
BENCH ?= .
# samples per bench for benchstat significance
COUNT ?= 6

run:
	go run .

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fuzz:
	go test -fuzz=Fuzz -fuzztime=30s .

bench:
	go test -bench='$(BENCH)' -count=$(COUNT) -benchmem -run=^$$ .
