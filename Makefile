# This Makefile is used for dev purposes
# Variables
REPO := ghcr.io/bnema/gordon
TAG := v2-dev
DEV_TAG := v2-dev-$(shell date +%Y%m%d-%H%M%S)
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
.PHONY: all build build-push clean dev-release test test-unit test-coverage test-race clean-test

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

# Create dev GitHub release (separate from GoReleaser)
dev-release: build
	@echo "Creating dev GitHub release..."
	@if [ -z "$(shell which gh)" ]; then \
		echo "Error: GitHub CLI (gh) is not installed. Please install it first."; \
		exit 1; \
	fi
	@echo "Creating dev release $(DEV_TAG)..."
	@gh release create $(DEV_TAG) \
		--title "Gordon Dev Build $(DEV_TAG)" \
		--notes "🚧 **Development Build** 🚧\n\nThis is an automated development build for testing purposes.\n\n**Commit:** $(COMMIT)\n**Build Date:** $(BUILD_DATE)\n\n⚠️ This is not a stable release. Use at your own risk." \
		--prerelease \
		--draft=false \
		$(DIST_DIR)/gordon-linux-amd64 \
		$(DIST_DIR)/gordon-linux-arm64
	@echo "Dev release created successfully!"
	@echo ""
	@echo "📦 Download URLs:"
	@echo "  AMD64: wget https://github.com/bnema/gordon/releases/download/$(DEV_TAG)/gordon-linux-amd64"
	@echo "  ARM64: wget https://github.com/bnema/gordon/releases/download/$(DEV_TAG)/gordon-linux-arm64"
	@echo ""
	@echo "🔗 Release page: https://github.com/bnema/gordon/releases/tag/$(DEV_TAG)"

# Test targets
test: test-unit

test-unit:
	go test -v ./...

test-race:
	go test -race -v ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"


clean-test:
	rm -f coverage.out coverage.html

# Clean up
clean:
	@echo "Cleaning up..."
	@rm -rf $(DIST_DIR)
	@$(ENGINE) system prune -f
	@echo "Cleanup completed."