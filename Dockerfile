# Gordon v2 - Production Dockerfile
# Multi-stage build for optimized container image

# Build stage
FROM golang:1.26.1-alpine3.22 AS builder

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
FROM alpine:3.22

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    curl \
    wget \
    tzdata \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN adduser -D -s /bin/sh gordon

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/gordon .

# Create data directory
RUN mkdir -p /data && chown gordon:gordon /data

# Copy default configuration (optional)
COPY --chown=gordon:gordon gordon.toml.example /app/gordon.toml.example

# Switch to non-root user
USER gordon

# Expose ports
EXPOSE 8088 5000

# Admin health is served on the registry/admin listener and is gated by auth in
# normal deployments, so this image does not declare a Docker healthcheck.

# Default command
CMD ["./gordon", "serve"]

# Metadata
LABEL maintainer="bnemam"
LABEL version="2.0"
LABEL description="Event-driven container deployment platform"
LABEL org.opencontainers.image.source="https://github.com/bnema/gordon"