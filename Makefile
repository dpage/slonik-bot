# slonik-bot: joins Google Meet calls, records captions and summarises them
#
#   make         build into ./bin
#   make test    run the Go tests
#   make lint    vet, gofmt and golangci-lint
#   make extension  copy the caption observer into the Chrome extension

SHELL      := /bin/bash
GO         ?= go
BIN        := bin
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS    := -X github.com/dpage/slonik-bot/internal/version.Version=$(VERSION) \
              -X github.com/dpage/slonik-bot/internal/version.Commit=$(COMMIT)

GOLANGCI_VERSION ?= v2.14.0

.PHONY: all build test lint fmt vet clean extension

all: build

## build: build the slonik-bot binary into ./bin
build:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN)/slonik-bot ./cmd/slonik-bot

## test: run the Go tests
test:
	$(GO) test ./...

## lint: vet, formatting and golangci-lint
lint: vet
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run

vet:
	$(GO) vet ./...

## extension: copy the caption observer into the Chrome extension, which
## has no build step of its own
extension:
	cp internal/captions/observer.js extension/observer.js

fmt:
	gofmt -w cmd internal

clean:
	rm -rf $(BIN)
