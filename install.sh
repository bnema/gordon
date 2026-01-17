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

# Download checksums file
CHECKSUMS_URL="https://github.com/${REPO}/releases/latest/download/checksums.txt"
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
