VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/QwikByte/noryx/internal/buildinfo.Version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
BUF     := go run github.com/bufbuild/buf/cmd/buf@v1.73.0
GORELEASER := go run github.com/goreleaser/goreleaser/v2@v2.18.2

.PHONY: build web master agent generate lint test packages dev-master dev-web

## build: build the panel and both binaries into bin/
build: web master agent

web:
	cd web && npm ci && npm run build

master:
	$(GOBUILD) -tags ui -o bin/noryx-master ./cmd/noryx-master

agent:
	$(GOBUILD) -o bin/noryx-agent ./cmd/noryx-agent

## generate: regenerate the gRPC code from api/**/*.proto
generate:
	$(BUF) lint
	$(BUF) generate

lint:
	golangci-lint run ./...
	cd web && npm run lint && npm run typecheck

test:
	go test -race ./...
	cd web && npm test

## packages: build the packages and archives of a release into dist/, without publishing
packages:
	$(GORELEASER) release --snapshot --clean

## dev-master: run the master with a local data directory (API on :8080, enrollment on :9443)
dev-master:
	go run ./cmd/noryx-master --data-dir .data/master serve --public-enroll-addr 127.0.0.1:9443

## dev-web: run the Vite dev server, proxying /api to the master
dev-web:
	cd web && npm run dev
