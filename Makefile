SHELL := /bin/bash

BINARY_NAME := agent-bridge
BINARY_DIR := .dist
BINARY_PATH := $(BINARY_DIR)/$(BINARY_NAME)
IMAGE_NAME ?= agent-bridge:local
CONTAINER_NAME ?= agent-bridge
PORT ?= 8192

.PHONY: build-binary build-container container-run

build-binary:
	@mkdir -p "$(BINARY_DIR)"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$(BINARY_PATH)" ./cmd/agent-bridge
	@echo "Built binary: $(BINARY_PATH)"

build-container:
	@test -f "$(BINARY_PATH)" || (echo "Missing $(BINARY_PATH). Run: make build-binary" && exit 1)
	docker build -f Containerfile -t "$(IMAGE_NAME)" .
	@echo "Built image: $(IMAGE_NAME)"

run-container:
	docker compose up -d
	@echo "Started container: $(CONTAINER_NAME) (port $(PORT))"
