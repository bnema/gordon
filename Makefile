# This Makefile is used for dev purposes
# Variables
REPO := ghcr.io/bnema/gordon
TAG := v3-dev
DEV_TAG := v3-dev-$(shell date +%Y%m%d-%H%M%S)
DIST_DIR := ./dist
ENGINE ?= podman

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
.PHONY: all build build-push clean dev-release \
	test test-short test-race test-coverage \
	lint fmt check mocks clean-test help

# Default target
all: build

##@ Development

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

fmt: ## Format Go code
	@echo "Formatting Go code..."
	@go fmt ./...

lint: ## Run golangci-lint
	@echo "Running linter..."
	@golangci-lint run ./...

mocks: ## Generate mocks using mockery
	@echo "Generating mocks..."
	@mockery
	@echo "Mocks generated successfully"

proto: ## Generate Go code from protobuf definitions
	@echo "Generating protobuf code..."
	@buf generate
	@echo "Protobuf code generated successfully"

check: lint test ## Run lint and tests

##@ Testing

test: ## Run all tests
	@echo "Running tests..."
	@go test ./...

test-v: ## Run all tests with verbose output
	@go test -v ./...

test-short: ## Run tests (skip long-running tests)
	@go test -short ./...

test-race: ## Run tests with race detector
	@echo "Running tests with race detector..."
	@go test -race ./...

test-coverage: ## Run tests with coverage report
	@echo "Running tests with coverage..."
	@go test -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

test-usecase: ## Run usecase layer tests only
	@go test -v ./internal/usecase/...

test-adapter: ## Run adapter layer tests only
	@go test -v ./internal/adapters/...

test-integration-build: build-local ## Build Gordon test image for integration tests
	@echo "Building Gordon test image..."
	@docker build -t gordon:v3-test .
	@echo "Test image built: gordon:v3-test"

test-integration: test-integration-build ## Run integration tests (max 10min)
	@echo "Running integration tests..."
	@docker pull ghcr.io/bnema/go-hello-world-http:latest || true
	@go test -v -timeout 10m ./tests/integration/... 2>&1 | tee test-integration.log
	@echo "Integration tests complete. Log: test-integration.log"

test-integration-quick: test-integration-build ## Run quick integration tests (startup + gRPC only, ~3min)
	@echo "Running quick integration tests..."
	@go test -v -timeout 5m -run "Test01|Test02" ./tests/integration/...

##@ Build

build: ## Build binaries for linux (amd64 and arm64)
	@echo "Building Go binaries..."
	@mkdir -p $(DIST_DIR)
	@rm -f $(DIST_DIR)/*
	@echo "Building with version $(VERSION), commit $(COMMIT), date $(BUILD_DATE)"
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/gordon-linux-amd64 ./main.go
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/gordon-linux-arm64 ./main.go
	@echo "Go binaries built successfully"

build-local: ## Build binary for current platform
	@echo "Building for current platform..."
	@go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/gordon ./main.go
	@echo "Binary built: $(DIST_DIR)/gordon"

##@ Release

build-push: build ## Build and push Docker images
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

dev-release: build ## Create dev GitHub release
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

##@ Cleanup

clean: ## Clean build artifacts
	@echo "Cleaning up..."
	@rm -rf $(DIST_DIR)
	@echo "Cleanup completed."

clean-all: clean clean-test ## Clean all artifacts including test files
	@$(ENGINE) system prune -f

clean-test: ## Clean test artifacts
	@rm -f coverage.out coverage.html