# This Makefile is used for dev purposes
# Variables
REPO := ghcr.io/bnema/gordon
TAG := dev
DIST_DIR := ./dist
ENGINE := podman

# Version information
VERSION := $(shell git describe --tags --always --dirty)
COMMIT := $(shell git rev-parse --short HEAD)
BUILD_DATE := $(shell date -u '+%Y-%m-%d_%I:%M:%S%p')

# Build flags
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(BUILD_DATE)

# Architectures
ARCHS := amd64 arm64

# Phony targets
.PHONY: all build build-push clean

# Default target
all: build

# Build binaries
build:
	@echo "Building Go binaries..."
	@mkdir -p $(DIST_DIR)
	@rm -f $(DIST_DIR)/*
	@echo "Building with version $(VERSION), commit $(COMMIT), date $(BUILD_DATE)"
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/gordon-linux-amd64 ./main.go
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/gordon-linux-arm64 ./main.go
	@echo "Go binaries built successfully"

# Build and push Docker images
build-push: build
	@echo "Cleaning up dangling images..."
	@$(ENGINE) image prune -f

	@echo "Building and pushing Docker images..."
	@for arch in $(ARCHS); do \
		cp $(DIST_DIR)/gordon-linux-$$arch gordon; \
		$(ENGINE) build -t $(REPO):$(TAG)-$$arch .; \
		rm gordon; \
		$(ENGINE) push $(REPO):$(TAG)-$$arch; \
	done

	@echo "Removing existing manifest..."
	@$(ENGINE) manifest rm $(REPO):$(TAG) || true

	@echo "Creating multi-arch manifest..."
	@$(ENGINE) manifest create $(REPO):$(TAG) \
		$(REPO):$(TAG)-amd64 \
		$(REPO):$(TAG)-arm64

	@echo "Annotating arm64 image..."
	@$(ENGINE) manifest annotate $(REPO):$(TAG) \
		$(REPO):$(TAG)-arm64 --arch arm64 --variant v8

	@echo "Inspecting manifest..."
	@$(ENGINE) manifest inspect $(REPO):$(TAG)

	@echo "Pushing multi-arch manifest..."
	@$(ENGINE) manifest push --all $(REPO):$(TAG)

	@echo "Script completed successfully."

# Clean up
clean:
	@echo "Cleaning up..."
	@rm -rf $(DIST_DIR)
	@$(ENGINE) system prune -f
	@echo "Cleanup completed."
