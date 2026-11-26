VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test e2e vet

build:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o bin/coop ./cmd/coop

test:
	go test -race ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

e2e: build
	./scripts/e2e.sh
