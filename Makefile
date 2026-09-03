VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint bench fmt-check tidy-check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/qurator ./cmd/qurator

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run ./...

bench:
	go test -run '^$$' -bench . -benchmem ./internal/qr/... ./internal/analytics/... ./internal/httpapi/...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

tidy-check:
	@cp go.mod /tmp/go.mod.bak && cp go.sum /tmp/go.sum.bak && go mod tidy && \
	  (diff -q go.mod /tmp/go.mod.bak && diff -q go.sum /tmp/go.sum.bak) || (echo "go mod tidy changed files"; exit 1)
