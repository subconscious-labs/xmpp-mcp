REPO := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

GO      = go
BIN     = bin/xmpp-mcp
# For local builds: use `git describe` so you get e.g. "v0.1.0-3-gabcdef" or
# "v0.1.0-dev" when there is no tag yet. Falls back to "dev" if git is absent.
VERSION := $(shell git describe --tags --always --dirty=-dev 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build clean deps fmt vet test check

all: build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

deps:
	$(GO) mod tidy
	$(GO) mod download

get:
	$(GO) get -v ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out
	$(GO) tool cover -html=coverage.out -o coverage.html

check: fmt vet test

clean:
	$(GO) clean -v
	rm -rf $(REPO)/bin coverage.out coverage.html
