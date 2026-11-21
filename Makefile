# Convenience targets for the pilot-go API. The Go toolchain must be on PATH.
GO ?= go

.PHONY: build test vet fmt run

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

run:
	$(GO) run .
