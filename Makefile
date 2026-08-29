.PHONY: help run test test-race test-nocontract test-all build-prod cover fuzz bench bench-contention bench-save bench-cmp

# contracts are compiled in by default; -tags nocontract drops them
PROD_TAGS = nocontract
# filter benches: make bench BENCH=GetParallel
BENCH ?= .
# samples per bench for benchstat significance
COUNT ?= 6
# package to fuzz/bench: make fuzz PKG=./sstable FUZZ=FuzzBlockDecode
PKG ?= .
FUZZ ?= Fuzz

help: ## show this help
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) \
		| sed 's/:.*##/\t/' \
		| awk -F'\t' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  variables: PKG=$(PKG)  FUZZ=$(FUZZ)  BENCH=$(BENCH)  COUNT=$(COUNT)"

run: ## go run the demo main
	go run ./cmd/tinylsm

test: ## run all tests
	go test ./...

test-race: ## run all tests under the race detector — the phase gate
	go test -race ./...

test-nocontract: ## run all tests with contracts compiled out
	go test -tags $(PROD_TAGS) ./...

test-all: test-race test-nocontract ## race + nocontract, both build configs

build-prod: ## build with contracts stripped
	go build -tags $(PROD_TAGS) -o bin/tinylsm ./cmd/tinylsm

cover: ## coverage report per function
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

fuzz: ## fuzz one package: make fuzz PKG=./sstable FUZZ=FuzzBlockDecode
	go test -fuzz='$(FUZZ)' -fuzztime=30s $(PKG)

bench: ## benchmark one package: make bench PKG=./sstable BENCH=Add
	go test -bench='$(BENCH)' -count=$(COUNT) -benchmem -run=^$$ $(PKG)
