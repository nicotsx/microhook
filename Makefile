.DEFAULT_GOAL := help

GO ?= go
MODULE := github.com/nicotsx/microhook
MAIN_PACKAGE := ./cmd/microhook
BUILDINFO_PACKAGE := $(MODULE)/internal/buildinfo
BINARY := microhook
BUILD_DIR := ./bin
OUTPUT := $(BUILD_DIR)/$(BINARY)
CONFIG ?= ./microhook.yml

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILT_BY ?= $(shell whoami)

LDFLAGS := -X $(BUILDINFO_PACKAGE).Version=$(VERSION)
LDFLAGS += -X $(BUILDINFO_PACKAGE).Commit=$(COMMIT)
LDFLAGS += -X $(BUILDINFO_PACKAGE).BuildTime=$(BUILD_TIME)
LDFLAGS += -X $(BUILDINFO_PACKAGE).BuiltBy=$(BUILT_BY)

.PHONY: help deps tidy fmt test build run validate-config version install clean

help:
	@printf "Targets:\n"
	@printf "  make deps               Download Go module dependencies\n"
	@printf "  make tidy               Sync go.mod and go.sum\n"
	@printf "  make fmt                Format all Go packages\n"
	@printf "  make test               Run the Go test suite\n"
	@printf "  make build              Build ./bin/$(BINARY)\n"
	@printf "  make run CONFIG=...     Run the service with a config file\n"
	@printf "  make validate-config CONFIG=...  Validate a config file\n"
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

run:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) serve -config $(CONFIG)

validate-config:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) validate-config -config $(CONFIG)

version:
	$(GO) run -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE) version

install:
	$(GO) install -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE)

clean:
	rm -rf $(BUILD_DIR)
