VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install test check-readme

build:
	go build $(LDFLAGS) -o bin/ticketvoice .

install:
	go build $(LDFLAGS) -o $(shell go env GOPATH)/bin/ticketvoice .

test:
	go test ./...

# Runs the tool's own gate against the one paragraph in README.md written in ticket-body register —
# the Why section — as if it were a Linear issue description. Matches cope's `make check-readme`
# (cope-gate --check README.md) and effigy's generate_readme.py: dogfood the mechanism on the docs,
# not a separate prose-quality pass by hand. The rest of the README is documentation, not a ticket,
# and would fail a 150-word budget by design — only this section is a fair target.
check-readme: build
	@awk '/^## Why$$/{f=1;next} /^## /{if (f) exit} f' README.md | ./bin/ticketvoice --check -
