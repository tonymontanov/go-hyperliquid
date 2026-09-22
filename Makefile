# go-hyperliquid — developer tasks.
# No target here touches the network except `smoke` (keyless, read-only).

GO ?= go

.PHONY: all fmt fmt-check vet lint test race bench vectors smoke tidy check

all: check

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test ./... -count=1

race:
	$(GO) test ./... -count=1 -race

# Hot-path benchmarks. House rule: a change on a hot path ships with before/after
# numbers (median of >= 3 runs, ns/op + allocs/op); allocs/op must not grow.
bench:
	$(GO) test ./types/... ./internal/... -run '^$$' -bench . -benchmem -count=3

# Regenerates internal/action/vectors_generated_test.go with the OFFICIAL Python
# SDK (offline). Needs: PYSDK=<clone of hyperliquid-python-sdk>, PYTHON=<venv python
# with eth-account msgpack eth-utils>.
vectors:
	@test -n "$(PYSDK)" -a -n "$(PYTHON)" || (echo "usage: make vectors PYSDK=... PYTHON=..."; exit 1)
	@{ sed -n '1,/^package action$$/p' internal/action/vectors_generated_test.go; echo; \
	   PYTHONPATH=$(PYSDK) $(PYTHON) scripts/gen-signing-vectors.py; } > internal/action/vectors_generated_test.go.tmp
	@mv internal/action/vectors_generated_test.go.tmp internal/action/vectors_generated_test.go
	gofmt -w internal/action/vectors_generated_test.go

# Keyless, read-only run against the public testnet API.
smoke:
	$(GO) run ./examples/market-data

tidy:
	$(GO) mod tidy

check: fmt-check vet race
