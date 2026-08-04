VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install test

build:
	go build $(LDFLAGS) -o bin/ticketvoice .

install:
	go build $(LDFLAGS) -o $(shell go env GOPATH)/bin/ticketvoice .

test:
	go test ./...
