# Build and test the practice-site server.
#   make test        unit tests (race detector off: CGO is disabled)
#   make build       static linux/amd64 binary -> dist/ruyi-site-linux-amd64
#   make allowlist   regenerate static-allowlist.txt from `git ls-files`
#   make acceptance  run the curl-based acceptance script against a mock brain
GO      ?= go
OUT     := dist/ruyi-site-linux-amd64
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)

.PHONY: all build test vet allowlist check-allowlist acceptance clean mockbrain

all: vet test build

build:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -buildvcs=false \
		-ldflags '-s -w' -o $(OUT) ./cmd/ruyi-site
	@file $(OUT) 2>/dev/null || true
	@sha256sum $(OUT)

mockbrain:
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -o dist/mockbrain ./cmd/mockbrain

vet:
	$(GO) vet ./...

test:
	CGO_ENABLED=0 $(GO) test ./... -count=1

allowlist:
	scripts/gen-allowlist.sh > static-allowlist.txt
	@echo "wrote static-allowlist.txt ($$(grep -vc '^#' static-allowlist.txt) files)"

check-allowlist:
	@scripts/gen-allowlist.sh | diff -u static-allowlist.txt - && echo "allowlist up to date"

acceptance: build mockbrain
	test/acceptance.sh

clean:
	rm -rf dist
