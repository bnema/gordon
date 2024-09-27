#!/bin/bash

set -e

REPO="ghcr.io/bnema/gordon"
TAG="dev"
DIST_DIR="./dist"

# Ensure dist directory exists
mkdir -p $DIST_DIR

# Clean up previous builds
rm -f $DIST_DIR/*

# Build Go binaries for multiple platforms with CGO_ENABLED=0
echo "Building Go binaries..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $DIST_DIR/gordon-linux-amd64 ./main.go
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o $DIST_DIR/gordon-linux-arm64 ./main.go

echo "Successfully built Go binaries"
