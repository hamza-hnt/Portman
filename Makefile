VERSION ?= $(shell cat VERSION)
PREFIX ?= $(HOME)/.local
GO ?= go
CGO_ENABLED ?= $(if $(filter Darwin,$(shell uname -s)),1,0)

.PHONY: build run test check install brew-install package test-install clean

build:
	mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/portman ./cmd/portman

run: build
	./bin/portman

test:
	$(GO) test -race ./...

check:
	@test -z "$$($(GO) fmt ./...)" || (echo 'Formatting was corrected; rerun make check'; exit 1)
	$(GO) vet ./...
	$(GO) test -race ./...

install:
	./install.sh --source --prefix "$(PREFIX)"

package:
	./scripts/package.sh

test-install: package
	python3 scripts/test-install.py

brew-install:
	./scripts/install-homebrew.sh

clean:
	rm -rf bin dist
