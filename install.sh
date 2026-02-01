#!/bin/sh
set -e

# Gordon installer script
# Usage: curl -fsSL https://gordon.bnema.dev/install | bash
# Usage with pre-release: curl -fsSL https://gordon.bnema.dev/install | GORDON_PRERELEASE=1 bash

REPO="bnema/gordon"
INSTALL_DIR="/usr/local/bin"
VERSION="${GORDON_VERSION:-latest}"

echo "Installing Gordon..."

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    linux)
        OS="linux"
        ;;
    darwin)
        OS="darwin"
        ;;
    *)
        echo "Error: Unsupported operating system: $OS"
        exit 1
        ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo "Error: Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

echo "Detected: ${OS}/${ARCH}"

# Construct download URL
TARBALL="gordon_${OS}_${ARCH}.tar.gz"

# Determine version to download
if [ "$VERSION" = "latest" ] && [ -n "$GORDON_PRERELEASE" ]; then
    echo "Finding latest pre-release..."
    # Get latest release (including pre-releases) from GitHub API
    RELEASE_DATA=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases?per_page=1" 2>/dev/null || echo "")
    if [ -z "$RELEASE_DATA" ]; then
        echo "Error: Failed to fetch release information from GitHub API"
        exit 1
    fi
    VERSION=$(echo "$RELEASE_DATA" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        echo "Error: Could not determine latest pre-release version"
        exit 1
    fi
    echo "Using pre-release version: ${VERSION}"
elif [ "$VERSION" = "latest" ]; then
    echo "Using latest stable release"
    # Resolve "latest" to actual tag name since GitHub download URLs require exact tags
    RELEASE_DATA=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || echo "")
    if [ -z "$RELEASE_DATA" ]; then
        echo "Error: Failed to fetch latest release information from GitHub API"
        exit 1
    fi
    VERSION=$(echo "$RELEASE_DATA" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        echo "Error: Could not determine latest stable version"
        exit 1
    fi
    echo "Resolved version: ${VERSION}"
else
    echo "Using version: ${VERSION}"
fi

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"

# Create temporary directory
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# Download checksums file
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
echo "Downloading checksums..."
if ! curl -fsSL "$CHECKSUMS_URL" -o "$TMP_DIR/checksums.txt"; then
    echo "Error: Failed to download checksums file"
    exit 1
fi

echo "Downloading ${TARBALL}..."
if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/$TARBALL"; then
    echo "Error: Failed to download ${TARBALL}"
    exit 1
fi

# Verify checksum
echo "Verifying checksum..."
EXPECTED_CHECKSUM=$(grep -E "[[:space:]]${TARBALL}\$" "$TMP_DIR/checksums.txt" | awk '{print $1}')
if [ -z "$EXPECTED_CHECKSUM" ]; then
    echo "Error: Could not find checksum for ${TARBALL} in checksums.txt"
    exit 1
fi

# Calculate actual checksum (works on both Linux and macOS)
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_CHECKSUM=$(sha256sum "$TMP_DIR/$TARBALL" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_CHECKSUM=$(shasum -a 256 "$TMP_DIR/$TARBALL" | awk '{print $1}')
else
    echo "Error: Neither 'sha256sum' nor 'shasum' was found. Cannot verify checksum."
    echo ""
    echo "Please install one of these tools:"
    echo "  Debian/Ubuntu: sudo apt-get install coreutils"
    echo "  Fedora/RHEL:   sudo dnf install coreutils"
    echo "  Alpine:        apk add coreutils"
    echo "  macOS:         shasum should be pre-installed"
    exit 1
fi

if [ "$EXPECTED_CHECKSUM" != "$ACTUAL_CHECKSUM" ]; then
    echo "Error: Checksum verification failed!"
    echo "Expected: $EXPECTED_CHECKSUM"
    echo "Actual:   $ACTUAL_CHECKSUM"
    echo "The downloaded file may be corrupted or tampered with."
    exit 1
fi
echo "Checksum verified successfully."

echo "Extracting..."
tar -xzf "$TMP_DIR/$TARBALL" -C "$TMP_DIR"

echo "Installing to ${INSTALL_DIR}..."
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP_DIR/gordon" "$INSTALL_DIR/gordon"
else
    echo "sudo required to install to ${INSTALL_DIR}"
    sudo mv "$TMP_DIR/gordon" "$INSTALL_DIR/gordon"
fi

# Verify installation
if command -v gordon >/dev/null 2>&1; then
    echo ""
    echo "Gordon installed successfully!"
    gordon version
else
    echo ""
    echo "Installation complete. You may need to add ${INSTALL_DIR} to your PATH."
fi
