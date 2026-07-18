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

# Compatibility harness foundation and policy guards. Keep this list anchored
# with each real slice so an absent or pending scenario cannot silently pass CI.
COMPAT_HARNESS_GUARDS := TestBuildOldAndNewUsesBaselineAndCurrentWorkingTreeWithoutBranchMutation|TestGoBuilderBuildsCandidateFromCurrentWorkingTree|TestGoBuilderBaselineUsesDetachedWorktreeAndDoesNotCheckoutCurrentBranch|TestRunnerReadinessSupportsCallbackTCPExitAndTimeout|TestRunnerStartWaitReadyStopAndLogs|TestStageSideFixtureCopiesConfigAndIsolatesHomeAndData|TestFixtureMetadata|TestScenarioDefinitions|TestScenarioPodmanRequirements|TestImplementedScenarioAllowlistIsExact|TestImplementedScenarioFilteringIsExplicitAndPendingIsFailSafe|TestMigrationAndSecurityScenariosDoNotSilentlyPass
# The proxy gate deliberately selects its three real routes, proxy/pending policy,
# builder, and report contract tests. Do not replace this with a broad package
# run: the Make recipe verifies every real route itself passed rather than skipped.
COMPAT_PROXY_REAL_TESTS := TestCompatibilityManagedHTTPRoute|TestCompatibilityExternalRoute|TestCompatibilityZeroDowntimeDrain|TestCompatibilityDistributedDrainProtocol|TestCompatibilityTrafficProtocolMatrix|TestCompatibilityTrafficProtocolFailClosed|TestSplitEdgeTLSCompatibilityExceptionIsExplicit
# Monolith protocol ownership is a single deterministic listener pass. The
# authenticated stream matrix runs three times to catch ephemeral-port and
# listener/goroutine cleanup leaks; neither gate permits skips.
COMPAT_TRAFFIC_MONOLITH_TESTS := TestCompatibilityTrafficProtocolMatrix|TestCompatibilityTrafficProtocolFailClosed|TestSplitEdgeTLSCompatibilityExceptionIsExplicit
COMPAT_TRAFFIC_STREAM_TESTS := TestCompatibilityTrafficGraphStreamMatrix
# This is intentionally a separate, exact real-binary invocation. It builds
# origin/main and HEAD, then starts each binary sequentially with fresh state.
COMPAT_TRAFFIC_BINARY_TESTS := TestCompatibilityTrafficProtocolBinaries
# The split drain gate is in-process but uses the production TCP/gRPC/HTTP
# adapters. It must pass exactly once and must never become an environmental skip.
COMPAT_RUNTIME_REAL_TESTS := TestCompatibilityDistributedDrainProtocol
# Registry is an exact Docker-backed old/new OCI gate.  JSON inspection rejects
# skipped or multiply-run scenarios, while the focused unit/integration checks
# exercise durable outbox replay and control-owned exactly-once suppression.
COMPAT_REGISTRY_REAL_TESTS := TestCompatibilityRegistryScenarios
# Current-only split roles are intentionally not compared to origin/main: the
# gate starts production registry/control processes and verifies delivery.
COMPAT_REGISTRY_SPLIT_TEST := TestCompatibilitySplitRegistryEventFlow
# Security is a current-candidate gate, not old/new baseline parity. The exact
# tests below must each pass twice without a skip; the edge test needs Docker to
# prove the candidate container has no runtime socket access.
COMPAT_SECURITY_REAL_TESTS := TestCompatibilitySecurityEdgeNoPodmanSocket|TestCompatibilitySecurityRegistryNoPodmanSocket|TestCompatibilitySecurityComponentEnvMinimization|TestCompatibilitySecurityMissingComponentTokenRejected|TestCompatibilitySecurityWrongComponentTokenRejected|TestCompatibilitySecurityWrongScopeComponentTokenRejected
COMPAT_SECURITY_HARNESS_GUARDS := TestScenarioDefinitions|TestScenarioPodmanRequirements|TestImplementedScenarioAllowlistIsExact|TestImplementedScenarioFilteringIsExplicitAndPendingIsFailSafe|TestMigrationAndSecurityScenariosDoNotSilentlyPass|TestCompareSideResultsRedactsNestedEmbeddedJSONInEveryArtifact|TestReportOutputs
COMPAT_PROXY_HARNESS_GUARDS := TestCompatibilityManagedHTTPRoutePreflight|TestManagedHTTPRouteScenarioDefinition|TestExternalRouteScenarioDefinition|TestExternalRouteSubnetIsSafeCGNAT|TestZeroDowntimeDrainScenarioDefinition|TestManagedHTTPRoutePublishedAddressRejectsNonLoopback|TestPendingProxyScenariosDoNotSilentlyPass|TestScenarioDefinitions|TestScenarioPodmanRequirements|TestImplementedScenarioAllowlistIsExact|TestImplementedScenarioFilteringIsExplicitAndPendingIsFailSafe|TestMigrationAndSecurityScenariosDoNotSilentlyPass|TestBuildOldAndNewUsesBaselineAndCurrentWorkingTreeWithoutBranchMutation|TestGoBuilderBuildsCandidateFromCurrentWorkingTree|TestGoBuilderSurfacesBoundedWorktreeCleanupFailures|TestGoBuilderBaselineUsesDetachedWorktreeAndDoesNotCheckoutCurrentBranch|TestCompareSidesAlwaysWritesActionableReportOnDiff|TestCompareSideResultsSerializesValidationFailuresBeforeReturningError|TestCompareSideResultsRedactsNestedEmbeddedJSONInEveryArtifact|TestNewReportNeverHasMoreFailuresThanChecks|TestReportOutputs
COMPAT_ARTIFACT_DIR ?= $(or $(GORDON_COMPAT_ARTIFACT_DIR),artifacts/compat)

# Phony targets
.PHONY: all build build-push clean dev-release \
	test test-short test-race test-coverage \
	lint fmt check mocks proto proto-check clean-test gitleaks help \
	compat-harness-config compat-harness-cli compat-harness-api compat-harness-registry \
	compat-harness-proxy compat-harness-traffic compat-harness-runtime compat-harness-migration compat-harness-security

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

gitleaks: ## Block on secrets in the current working tree
	@gitleaks detect --source . --config .gitleaks.toml --no-git --redact

mocks: ## Generate mocks using mockery
	@echo "Generating mocks..."
	@mockery
	@echo "Mocks generated successfully"

proto: ## Generate protobuf bindings
	@echo "Generating protobuf bindings..."
	@buf generate

proto-check: proto ## Verify generated protobuf bindings are up to date
	@git diff --exit-code -- api/gordon
	@test -z "$$(git ls-files --others --exclude-standard -- api/gordon)" || \
		(echo "Untracked generated files under api/gordon:"; \
		 git ls-files --others --exclude-standard -- api/gordon; \
		 exit 1)

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

##@ Compatibility Harness

compat-harness-config: ## Run config compatibility harness checks
	@echo "Baseline ref: $${GORDON_COMPAT_BASELINE_REF:-origin/main}"
	@echo "Report path: $(COMPAT_ARTIFACT_DIR)/config/compat-report.json"
	@echo "Rerun: GORDON_COMPAT_ARTIFACT_DIR=$(COMPAT_ARTIFACT_DIR) GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=$${GORDON_COMPAT_BASELINE_REF:-origin/main} go test ./internal/testutils/compatoldnew -run '^TestCompatibilityConfigShowJSON$$' -count=1"
	@echo "Running config compatibility harness slice and policy guards..."
	@GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 go test ./internal/testutils/compatoldnew -run '^(TestCompatibilityConfigShowJSON|$(COMPAT_HARNESS_GUARDS))$$' -count=1

compat-harness-cli: ## Run CLI compatibility harness checks
	@echo "Baseline ref: $${GORDON_COMPAT_BASELINE_REF:-origin/main}"
	@echo "Report path: $(COMPAT_ARTIFACT_DIR)/cli/compat-report.json"
	@echo "Rerun: GORDON_COMPAT_ARTIFACT_DIR=$(COMPAT_ARTIFACT_DIR) GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_BASELINE_REF=$${GORDON_COMPAT_BASELINE_REF:-origin/main} go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRoutesListJSON$$' -count=1"
	@echo "Running CLI compatibility harness slice and policy guards..."
	@GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 go test ./internal/testutils/compatoldnew -run '^(TestCompatibilityRoutesListJSON|$(COMPAT_HARNESS_GUARDS))$$' -count=1

compat-harness-api: ## Run API compatibility harness checks
	@echo "Baseline ref: $${GORDON_COMPAT_BASELINE_REF:-origin/main}"
	@echo "Report path: $(COMPAT_ARTIFACT_DIR)/api/compat-report.json"
	@echo "Rerun: GORDON_COMPAT_ARTIFACT_DIR=$(COMPAT_ARTIFACT_DIR) GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=$${GORDON_COMPAT_BASELINE_REF:-origin/main} go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$$' -count=1"
	@echo "Running Docker preflight, API compatibility slice, and policy guards..."
	@docker info
	@GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test ./internal/testutils/compatoldnew -run '^(TestCompatibilityAdminAPIPreflight|TestCompatibilityAdminAuthAndRouteCRUD|$(COMPAT_HARNESS_GUARDS))$$' -count=1

compat-harness-registry: ## Run blocking old/new OCI registry and event-sync compatibility gates
	@echo "Running exact Docker-backed registry compatibility gate without skips..."
	@docker info
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_REGISTRY_REAL_TESTS)|TestScenarioDefinitions|TestScenarioPodmanRequirements|TestImplementedScenarioAllowlistIsExact|TestMigrationAndSecurityScenariosDoNotSilentlyPass)$$' -count=1 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_REGISTRY_REAL_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 1) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "registry compatibility test $$test did not pass exactly once or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test -json ./internal/testutils/compatoldnew -run '^$(COMPAT_REGISTRY_SPLIT_TEST)$$' -count=2 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		if ! jq -se --arg test "$(COMPAT_REGISTRY_SPLIT_TEST)" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 2) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
			echo "split registry compatibility test did not pass exactly twice or was skipped; refusing to pass the gate"; exit 1; \
		fi
	@go test ./internal/usecase/registry -run 'TestRegistryImagePushedEventContract' -count=1
	@go test ./internal/adapters/in/http/registry -run 'TestRegistryHTTPCompatibilityContract' -count=1
	@go test ./internal/adapters/out/eventoutbox -run '^TestOutboxPersistsOutageAndReplaysAfterRestart$$' -count=1
	@go test ./internal/usecase/controlplane -run '^(TestEventDispatcherRetriesFailureAndDeduplicatesSuccess|TestEventDispatcherManualIntentSurvivesControlRestart|TestEventDispatcherManualIntentSuppressesOnlyMatchingPush)$$' -count=1

compat-harness-traffic: ## Run Linux TLS/UDP traffic listener ownership preflight
	@echo "Running monolith protocol matrix once and authenticated stream matrix three times without skips..."
	@docker info
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_TRAFFIC_MONOLITH_TESTS))$$' -count=1 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_TRAFFIC_MONOLITH_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 1) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "monolith traffic compatibility test $$test did not pass exactly once or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_TRAFFIC_STREAM_TESTS))$$' -count=3 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_TRAFFIC_STREAM_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 3) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "stream traffic compatibility test $$test did not pass exactly three times or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_TRAFFIC_BINARY_TESTS))$$' -count=1 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_TRAFFIC_BINARY_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 1) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "traffic binary compatibility test $$test did not pass exactly once or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done

compat-harness-proxy: ## Run the blocking Docker proxy compatibility gate
	@echo "Baseline ref: $${GORDON_COMPAT_BASELINE_REF:-origin/main}"
	@echo "Report paths: $(COMPAT_ARTIFACT_DIR)/proxy{,-external,-zero-drain}/compat-report.json"
	@echo "Rerun: GORDON_COMPAT_ARTIFACT_DIR=$(COMPAT_ARTIFACT_DIR) GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=$${GORDON_COMPAT_BASELINE_REF:-origin/main} go test ./internal/testutils/compatoldnew -run '^($(COMPAT_PROXY_REAL_TESTS))$$' -count=1"
	@echo "Running required Docker preflight and blocking proxy compatibility slices..."
	@docker info
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_PROXY_REAL_TESTS)|$(COMPAT_PROXY_HARNESS_GUARDS))$$' -count=1 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_PROXY_REAL_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 1) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "proxy compatibility test $$test did not pass exactly once or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done
	@go test ./internal/usecase/proxy -run 'TestProxyTargetResolutionContract|TestDrainRegistryInFlight|TestDrainRegistryInFlightTimeout|TestService_InvalidateTarget|TestContainerDeployedHandler_Handle_InvalidatesCache' -count=1
	@go test ./internal/usecase/container -run 'TestService_ReconcileRemovedRoute_InvalidatesProxyCacheAndMetric' -count=1
	@go test ./internal/adapters/in/traffic -run 'TestUDPRemovedRouterWithRetainedEntryPointDrainsSession|TestUDPBackendChangeDrainsExistingSession|TestUDPRemovedRouterDrainsThenClosesSessions|TestTLSHTTPListenerCloseDrainsQueuedConnections|TestTCPPassthroughDrainWaitsForActiveConnectionThenTimesOut' -count=1

compat-harness-runtime: ## Run runtime compatibility harness checks
	@echo "Running runtime compatibility harness checks..."
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_RUNTIME_REAL_TESTS)|TestScenarioDefinitions|TestScenarioPodmanRequirements|TestMigrationAndSecurityScenariosDoNotSilentlyPass)$$' -count=1 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_RUNTIME_REAL_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 1) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "runtime compatibility test $$test did not pass exactly once or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done
	@go test ./internal/usecase/container -run 'TestRuntimeContract' -count=1
	@go test ./internal/adapters/out/docker -run 'TestRuntimeAdapterContract' -count=1

compat-harness-migration: ## Run migration prepare and rootless-Podman availability checks
	@echo "Running deterministic Docker-compatible migration prepare/health fixture checks..."
	@go test ./internal/app -run '^(TestComponentLauncherPlan|TestRuntimeComponentLauncher|TestMigrationOrchestrator)' -count=1
	@go test ./internal/testutils/compatoldnew -run '^(TestScenarioDefinitions|TestScenarioPodmanRequirements|TestMigrationAndSecurityScenariosDoNotSilentlyPass)$$' -count=1
	@if [ "$${GORDON_COMPAT_PODMAN:-0}" = "1" ]; then \
		echo "Verifying rootless Podman migration gate..."; \
		test "$$(id -u)" -ne 0; \
		podman info --format '{{.Host.Security.Rootless}}' | grep -qx true; \
	fi

compat-harness-security: ## Run blocking current-security compatibility gates
	@echo "Report paths: $(COMPAT_ARTIFACT_DIR)/security-{edge,registry,auth-missing,auth-wrong-component,auth-scope}/compat-report.json"
	@echo "Rerun: GORDON_COMPAT_ARTIFACT_DIR=$(COMPAT_ARTIFACT_DIR) GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test ./internal/testutils/compatoldnew -run '^($(COMPAT_SECURITY_REAL_TESTS))$$' -count=2"
	@echo "Running required Docker preflight and exact current-security gates..."
	@docker info
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT HUP INT TERM; \
		GORDON_COMPAT_ARTIFACT_DIR="$(COMPAT_ARTIFACT_DIR)" GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 go test -json ./internal/testutils/compatoldnew -run '^($(COMPAT_SECURITY_REAL_TESTS)|$(COMPAT_SECURITY_HARNESS_GUARDS))$$' -count=2 > "$$output"; status=$$?; \
		cat "$$output"; \
		if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
		for test in $$(printf '%s' '$(COMPAT_SECURITY_REAL_TESTS)' | tr '|' ' '); do \
			if ! jq -se --arg test "$$test" '([.[] | select(.Test == $$test and .Action == "pass")] | length == 2) and ([.[] | select(.Test == $$test and .Action == "skip")] | length == 0)' "$$output" >/dev/null; then \
				echo "security compatibility test $$test did not pass exactly twice or was skipped; refusing to pass the gate"; exit 1; \
			fi; \
		done

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