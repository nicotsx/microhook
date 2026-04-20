.DEFAULT_GOAL := help

GO ?= go
MODULE := github.com/nicotsx/microhook
MAIN_PACKAGE := ./cmd/microhook
BUILDINFO_PACKAGE := $(MODULE)/internal/buildinfo
BINARY := microhook
BUILD_DIR := ./bin
OUTPUT := $(BUILD_DIR)/$(BINARY)
RELEASE_DIR := ./dist
DOCKER_IMAGE ?= microhook:local
CONFIG ?= ./microhook.yml

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILT_BY ?= $(shell whoami)

LDFLAGS := -X $(BUILDINFO_PACKAGE).Version=$(VERSION)
LDFLAGS += -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)
LDFLAGS += -X $(BUILDINFO_PACKAGE).BuildTime=$(BUILD_TIME)
LDFLAGS += -X $(BUILDINFO_PACKAGE).BuiltBy=$(BUILT_BY)

.PHONY: help deps tidy fmt test build run validate-config version generate-token release-artifacts docker-build install-smoke verify-systemd release-check install clean

help:
	@printf "Targets:\n"
	@printf "  make deps               Download Go module dependencies\n"
	@printf "  make tidy               Sync go.mod and go.sum\n"
	@printf "  make fmt                Format all Go packages\n"
	@printf "  make test               Run the Go test suite\n"
	@printf "  make build              Build ./bin/$(BINARY)\n"
	@printf "  make release-artifacts  Build Linux release tarballs\n"
	@printf "  make docker-build       Build the Docker image\n"
	@printf "  make install-smoke      Run the Linux install smoke test\n"
	@printf "  make verify-systemd     Verify the packaged systemd unit\n"
	@printf "  make release-check      Run tests and release-readiness checks\n"
	@printf "  make run CONFIG=...     Run the service with a config file\n"
	@printf "  make validate-config CONFIG=...  Validate a config file\n"
	@printf "  make generate-token     Print a new bearer token\n"
	@printf "  make version            Print build metadata\n"
	@printf "  make install            Install the binary with go install\n"
	@printf "  make clean              Remove local build artifacts\n"

deps:
	$(GO) mod download

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

build:
	mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(OUTPUT) $(MAIN_PACKAGE)

release-artifacts:
	VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_TIME="$(BUILD_TIME)" BUILT_BY="$(BUILT_BY)" MODULE="$(MODULE)" BUILDINFO_PACKAGE="$(BUILDINFO_PACKAGE)" MAIN_PACKAGE="$(MAIN_PACKAGE)" BINARY="$(BINARY)" RELEASE_DIR="$(RELEASE_DIR)" ./scripts/build-release.sh

docker-build:
	docker build --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" --build-arg BUILT_BY="$(BUILT_BY)" -t "$(DOCKER_IMAGE)" .

run:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) serve -config $(CONFIG)

validate-config:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) validate-config -config $(CONFIG)

version:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) version

generate-token:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) generate-token

install-smoke:
	./scripts/install-smoke.sh "$(RELEASE_DIR)/$(BINARY)_$(VERSION)_linux_amd64.tar.gz"

verify-systemd:
	./scripts/verify-systemd.sh

release-check:
	$(MAKE) test
	$(MAKE) release-artifacts VERSION="$(VERSION)"
	$(MAKE) install-smoke VERSION="$(VERSION)"
	$(MAKE) verify-systemd

install:
	$(GO) install -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE)

clean:
	rm -rf $(BUILD_DIR) $(RELEASE_DIR)
