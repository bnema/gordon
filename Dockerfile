# Gordon v2 - Production Dockerfile
# Multi-stage build for optimized container image

# Build stage
FROM golang:1.24-alpine AS builder

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
EXPOSE 8080 5000

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

# Default command
CMD ["./gordon", "start"]

# Metadata
LABEL maintainer="bnemam"
LABEL version="2.0"
LABEL description="Event-driven container deployment platform"
LABEL org.opencontainers.image.source="https://github.com/bnema/gordon"