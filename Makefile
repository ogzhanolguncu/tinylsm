.PHONY: run test test-race cover fuzz bench

run:
	go run .

test:
	go test -v ./...

test-race:
	go test -race ./...

cover:
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fuzz:
	go test -fuzz=Fuzz -fuzztime=30s .

bench:
	go test -bench=. -benchmem -run=^$$ .
