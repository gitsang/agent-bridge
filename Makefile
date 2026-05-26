SHELL := /usr/bin/env bash

# metadata
PROJECT ?= agent-bridge

# version
VERSION    ?= $(shell git describe --tags --always --match "v*" --dirty 2>/dev/null || git rev-parse --short HEAD)
BUILD_DATE ?= $(shell date -u --iso-8601=seconds)
GIT_COMMIT := $(shell git rev-parse HEAD)

# dist
OS          ?= linux
ARCH        ?= amd64
DIST        ?= dist/$(PROJECT)/$(OS)/$(ARCH)
DIST_BIN    ?= $(DIST)/bin

# go
GO          := go
GO_VERSION  := $(shell $(GO) version | awk '{print $$3}')
GO_MODULE   := $(shell awk '/^module /{print $$2}' go.mod)
GO_OS       := $(if $(GOOS),$(GOOS),$(shell $(GO) env GOOS))
GO_ARCH     := $(if $(GOARCH),$(GOARCH),$(shell $(GO) env GOARCH))
GO_CMD_PATH ?= ./cmd/agent-bridge

# build
LD_FLAGS ?= -s -w
LD_FLAGS += -X "main.Version=$(VERSION)"
LD_FLAGS += -X "main.BuildDate=$(BUILD_DATE)"
LD_FLAGS += -X "main.GitCommit=$(shell git rev-parse HEAD)"
LD_FLAGS += -X "main.GoVersion=$(GO_VERSION)"
LD_FLAGS += -X "main.GOOS=$(GO_OS)"
LD_FLAGS += -X "main.GOARCH=$(GO_ARCH)"

# container
CONTAINER_CLI   ?= docker
CONTAINER_FILE  ?= Containerfile
CONTAINER_IMAGE ?= agent-bridge:local


#------------------------------------------------------------------------------#
##@ Prerequisites
#------------------------------------------------------------------------------#


BUILD_DIR = $(PWD)/.builds
BUILD_BIN = $(BUILD_DIR)/bin
$(BUILD_BIN):
	@mkdir -p $(BUILD_BIN)
export PATH := $(BUILD_BIN):$(PATH)


# goimports
# https://pkg.go.dev/golang.org/x/tools/cmd/goimports
GOIMPORTS         = $(BUILD_BIN)/goimports
GOIMPORTS_VERSION ?= latest
$(GOIMPORTS): $(BUILD_BIN)
ifeq ($(wildcard $(GOIMPORTS)),)
	GOBIN=$(BUILD_BIN) CGO_ENABLED=0 $(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)
endif


# golangci-lint
# https://github.com/golangci/golangci-lint
GOLANGCI_LINT         = $(BUILD_BIN)/golangci-lint
GOLANGCI_LINT_VERSION ?= latest
$(GOLANGCI_LINT): $(BUILD_BIN)
ifeq ($(wildcard $(GOLANGCI_LINT)),)
	GOBIN=$(BUILD_BIN) CGO_ENABLED=0 $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
endif


PREREQUISITES = $(GOIMPORTS) \
				$(GOLANGCI_LINT)


.PHONY: init
## init development env
init: $(PREREQUISITES)
	@echo "All prerequisites installed."


#------------------------------------------------------------------------------#
##@ CodeQL
#------------------------------------------------------------------------------#


.PHONY: fmt
## format go code by goimports
fmt: $(GOIMPORTS)
	@$(GOIMPORTS) -w $(shell find . -name "*.go" -not -path "*/.builds/*" -not -path "*/dist/*" | LC_COLLATE=C sort)


.PHONY: lint
## run golangci-lint linter
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run


.PHONY: lint-fix
## run golangci-lint linter and perform fixes
lint-fix: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run --fix


.PHONY: vet
## vet packages
vet:
	$(GO) vet ./...


#------------------------------------------------------------------------------#
##@ Debug
#------------------------------------------------------------------------------#


.PHONY: test
## run unit tests
test:
	CGO_ENABLED=0 $(GO) test -race ./...


.PHONY: run
## run via go run
run:
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) \
		$(GO) run \
		-trimpath \
		-ldflags "$(LD_FLAGS)" \
		$(GO_CMD_PATH) \
		$(filter-out $@,$(MAKECMDGOALS))


.PHONY: run-container
## run via container compose
run-container:
	$(CONTAINER_CLI) compose up -d


#------------------------------------------------------------------------------#
##@ Build
#------------------------------------------------------------------------------#


$(DIST_BIN):
	@mkdir -p $@


.PHONY: build-binary
## build binary
build-binary: $(DIST_BIN)
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) \
		$(GO) build \
		-trimpath \
		-ldflags "$(LD_FLAGS)" \
		-o "$(DIST_BIN)/$(PROJECT)" \
		$(GO_CMD_PATH)
	@echo "Built binary: $(DIST_BIN)/$(PROJECT)"


.PHONY: build-container
## build container image
build-container: build-binary
	$(CONTAINER_CLI) build \
		--build-arg DIST_PATH="$(DIST_BIN)/$(PROJECT)" \
		-f "$(CONTAINER_FILE)" \
		-t "$(CONTAINER_IMAGE)" .
	@echo "Built image: $(CONTAINER_IMAGE)"


.PHONY: build
## build binary and container image
build: build-binary build-container


.PHONY: publish
## publish container image
publish:
	$(CONTAINER_CLI) push "$(CONTAINER_IMAGE)"


#------------------------------------------------------------------------------#
##@ Clean
#------------------------------------------------------------------------------#


.PHONY: clean
## clean generated build artifacts
clean:
	rm -rf dist .builds


#------------------------------------------------------------------------------#
##@ Help
#------------------------------------------------------------------------------#


.PHONY: help
## display help
help:
	@awk 'BEGIN \
	{ \
		FS = ":.*##"; \
		printf "\nUsage:\n  make \033[36m<target>\033[0m\n" \
	} \
	/^[0-9a-zA-Z_\-\/]+:/ \
	{ \
		helpMessage = match(lastLine, /^## (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
			printf "  \033[36m%-24s\033[0m %s\n", helpCommand, helpMessage; \
		} \
	} { lastLine = $$0 } \
	/^##@/ \
	{ \
		printf "\n\033[1m%s\033[0m\n", substr($$0, 5) \
	} ' $(MAKEFILE_LIST)


.DEFAULT_GOAL := help
