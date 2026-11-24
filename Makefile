# Shared gates (STANDARD.md MOD-5).
include standard.mk

# Repository-specific gates appended to `make check`.
CHECK_EXTRA := modverify cross fuzz bench example

# Bounded fuzz budgets; every failure is fatal.
FUZZ_EXECUTIONS ?= 100000x

.PHONY: modverify cross fuzz bench example

modverify:
	$(GO) mod verify

# Windows must compile its tests; FreeBSD must vet.
cross:
	GOOS=windows GOARCH=amd64 $(GO) test -run '^$$' -exec true ./...
	GOOS=freebsd GOARCH=amd64 $(GO) vet ./...

fuzz:
	$(GO) test -run '^$$' -fuzz '^FuzzFindChunks$$' -fuzztime=$(FUZZ_EXECUTIONS) -parallel=2 ./internal/scan
	$(GO) test -run '^$$' -fuzz '^FuzzFilterChunks$$' -fuzztime=$(FUZZ_EXECUTIONS) -parallel=2 ./internal/redact
	$(GO) test -run '^$$' -fuzz '^FuzzFilterChunks$$' -fuzztime=$(FUZZ_EXECUTIONS) -parallel=2 ./internal/vt
	$(GO) test -run '^$$' -fuzz '^FuzzSessionChunks$$' -fuzztime=$(FUZZ_EXECUTIONS) -parallel=2 .
	$(GO) test -run '^$$' -fuzz '^FuzzCharsetChunks$$' -fuzztime=$(FUZZ_EXECUTIONS) -parallel=2 .

bench:
	$(GO) test -run '^$$' -bench . -benchmem -benchtime=3x ./...

example:
	EXPECT_RECORD=off $(GO) run ./examples/dialogue
