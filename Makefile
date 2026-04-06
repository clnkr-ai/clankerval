VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build
.PHONY: build test evaluations man _check-man clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o clankerval ./cmd/clankerval

test:
	go test -race ./... -v

evaluations:
	mkdir -p .tmp
	go build -trimpath -o ./.tmp/evalfixture-agent ./internal/testfixture/evalfixture-agent
	go run ./cmd/clankerval run --suite dummy --evals-dir ./testdata/evaluations --binary $(CURDIR)/.tmp/evalfixture-agent --output-dir ./.tmp/evaluations

man:
	go-md2man -in doc/clankerval.1.md -out doc/clankerval.1

_check-man:
	@tmp=$$(mktemp); \
	go-md2man -in doc/clankerval.1.md -out $$tmp; \
	diff -u doc/clankerval.1 $$tmp >/dev/null 2>&1 || (echo "error: doc/clankerval.1 is out of date; run 'make man'" >&2; rm -f $$tmp; exit 1); \
	rm -f $$tmp; \
	echo "man page is up-to-date"

clean:
	rm -f clankerval
	rm -rf .tmp
