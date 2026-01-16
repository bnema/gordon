#!/bin/sh
set -e

# Gordon installer script
# Usage: curl -fsSL https://gordon.bnema.dev/install | bash

REPO="bnema/gordon"
INSTALL_DIR="/usr/local/bin"

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
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${TARBALL}"

# Create temporary directory
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${TARBALL}..."
curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/$TARBALL"

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
