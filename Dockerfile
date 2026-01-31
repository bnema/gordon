# Gordon v3 - Production Dockerfile
# Multi-stage build for optimized container image
# Supports 4 component modes: core, proxy, registry, secrets

# Build stage
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o gordon .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
# - docker-cli: For core component to manage containers
# - pass, gnupg: For secrets component (password-store backend)
# - ca-certificates: TLS support
# - curl, wget: Health checks and debugging
# - tzdata: Timezone support
RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    curl \
    wget \
    tzdata \
    pass \
    gnupg \
    && rm -rf /var/cache/apk/*

# Create non-root user (not used by default - components run as root for Docker socket access)
RUN adduser -D -s /bin/sh gordon

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/gordon .

# Create data directory
RUN mkdir -p /data && chown gordon:gordon /data

# Copy default configuration
COPY --chown=gordon:gordon gordon.toml.example /app/gordon.toml

# Environment variables
# GORDON_COMPONENT: Which component to run (core|proxy|registry|secrets)
# GORDON_IMAGE: Self-image reference for sub-container deployment
# HEALTHCHECK_PORT: Port for health checks (varies by component)
ENV GORDON_COMPONENT="" \
    GORDON_IMAGE="" \
    HEALTHCHECK_PORT="5000" \
    GORDON_LOG_LEVEL="info"

# Expose ports by component:
# - gordon-core: 5000 (admin API), 9090 (gRPC)
# - gordon-proxy: 80 (HTTP)
# - gordon-registry: 5000 (registry HTTP), 9092 (gRPC)
# - gordon-secrets: 9091 (gRPC)
EXPOSE 80 5000 9090 9091 9092

# Health check (component-aware)
# Core/Registry use port 5000 by default
# Proxy uses port 80
# Secrets uses gRPC health on 9091
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:${HEALTHCHECK_PORT}/v2/health || \
        wget --quiet --tries=1 --spider http://localhost:${HEALTHCHECK_PORT}/health || \
        exit 1

# Use ENTRYPOINT so we can pass arguments (like --component) after the command
# For v3 sub-containers, use: docker run gordon:v3-test --component=core
ENTRYPOINT ["./gordon", "serve"]
CMD []

# Metadata
LABEL maintainer="bnemam"
LABEL version="3.0"
LABEL description="Event-driven container deployment platform - v3 sub-container architecture"
LABEL org.opencontainers.image.source="https://github.com/bnema/gordon"
LABEL gordon.components="core,proxy,registry,secrets"
