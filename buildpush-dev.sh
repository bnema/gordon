#!/bin/bash

set -e

REPO="ghcr.io/bnema/gordon"
TAG="dev"
DIST_DIR="./dist"

# Ensure dist directory exists
mkdir -p $DIST_DIR

# Build Go binaries for multiple platforms with CGO_ENABLED=0
echo "Building Go binaries..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $DIST_DIR/gordon-linux-amd64 ./cmd/cli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o $DIST_DIR/gordon-linux-arm64 ./cmd/cli

# Build Docker images for each architecture
echo "Building Docker images..."
docker build -t $REPO:${TAG}-amd64 --build-arg ARCH=amd64 -f Dockerfile .
docker build -t $REPO:${TAG}-arm64v8 --build-arg ARCH=arm64 -f Dockerfile .

# Push images
echo "Pushing Docker images..."
docker push $REPO:${TAG}-amd64
docker push $REPO:${TAG}-arm64v8

# Create and push multi-arch manifest
echo "Creating and pushing multi-arch manifest..."
docker manifest create $REPO:$TAG \
  $REPO:${TAG}-amd64 \
  $REPO:${TAG}-arm64v8 \
  --amend

# Annotate the arm64 image with variant information
docker manifest annotate $REPO:$TAG \
  $REPO:${TAG}-arm64v8 --arch arm64 --variant v8

docker manifest push $REPO:$TAG
