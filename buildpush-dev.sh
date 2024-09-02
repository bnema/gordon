#!/bin/bash
set -e
ENGINE="podman"
REPO="ghcr.io/bnema/gordon"
TAG="dev"
DIST_DIR="./dist"

# Function to handle errors
handle_error() {
    echo "Error occurred. Cleaning up..."
    $ENGINE system prune -f
    exit 1
}

# Set up error handling
trap 'handle_error' ERR

# Ensure dist directory exists
mkdir -p $DIST_DIR

# Build Go binaries for multiple platforms with CGO_ENABLED=0
echo "Building Go binaries..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $DIST_DIR/gordon-linux-amd64 ./cmd/cli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o $DIST_DIR/gordon-linux-arm64 ./cmd/cli

# Clean up any dangling images before building
echo "Cleaning up dangling images..."
$ENGINE image prune -f

# Build Docker images for each architecture
echo "Building Docker images..."
for ARCH in amd64 arm64; do
    # Copy the appropriate binary to the root directory and rename it to 'gordon'
    cp $DIST_DIR/gordon-linux-$ARCH gordon

    # Build the Docker image
    $ENGINE build -t $REPO:${TAG}-$ARCH .

    # Remove the temporary 'gordon' file
    rm gordon
done

# Push images
echo "Pushing Docker images..."
$ENGINE push $REPO:${TAG}-amd64
$ENGINE push $REPO:${TAG}-arm64

# Remove existing manifest if it exists
echo "Removing existing manifest..."
$ENGINE manifest rm $REPO:$TAG || true

# Create multi-arch manifest
echo "Creating multi-arch manifest..."
$ENGINE manifest create $REPO:$TAG \
  $REPO:${TAG}-amd64 \
  $REPO:${TAG}-arm64

# Annotate the arm64 image with variant information
echo "Annotating arm64 image..."
$ENGINE manifest annotate $REPO:$TAG \
  $REPO:${TAG}-arm64 --arch arm64 --variant v8

# Debug: List manifests
echo "Listing manifests..."
$ENGINE manifest inspect $REPO:$TAG

# Push multi-arch manifest
echo "Pushing multi-arch manifest..."
$ENGINE manifest push --all $REPO:$TAG

echo "Script completed successfully."
