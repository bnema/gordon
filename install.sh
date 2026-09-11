#!/bin/sh
set -e

# Gordon installer script
# Usage: curl -fsSL https://gordon.bnema.dev/install | sh
# Usage with pre-release: curl -fsSL https://gordon.bnema.dev/install | GORDON_PRERELEASE=1 sh
# Usage with next source build: curl -fsSL https://gordon.bnema.dev/install | GORDON_CHANNEL=next sh

REPO="bnema/gordon"
VERSION="${GORDON_VERSION:-latest}"
CHANNEL="${GORDON_CHANNEL:-stable}"

case "${GORDON_UPDATE_PATH:-}" in
    ''|0|1) ;;
    *) echo "Error: GORDON_UPDATE_PATH must be 0 or 1"; exit 1 ;;
esac

if [ -n "${GORDON_INSTALL_DIR+x}" ]; then
    INSTALL_DIR=$GORDON_INSTALL_DIR
    if [ -z "$INSTALL_DIR" ]; then
        echo "Error: GORDON_INSTALL_DIR must not be empty"
        exit 1
    fi
else
    if [ -n "${SUDO_USER:-}" ] || [ "$(id -u)" -eq 0 ]; then
        echo "Error: Refusing a default user-local install while running as root or through sudo."
        echo "Run the installer as the target user, or set GORDON_INSTALL_DIR explicitly for a global install."
        exit 1
    fi
    if [ -z "${HOME:-}" ]; then
        echo "Error: HOME is required for the default installation directory"
        exit 1
    fi
    INSTALL_DIR=$HOME/.local/bin
fi

case "$INSTALL_DIR" in
    /*) ;;
    *) echo "Error: GORDON_INSTALL_DIR must be an absolute path"; exit 1 ;;
esac
if [ "$INSTALL_DIR" != "/" ]; then
    while [ "${INSTALL_DIR%/}" != "$INSTALL_DIR" ]; do
        INSTALL_DIR=${INSTALL_DIR%/}
    done
fi
INSTALL_DIR_WITHOUT_CONTROLS=$(LC_ALL=C printf '%sX' "$INSTALL_DIR" | LC_ALL=C tr -d '\000-\037\177')
if [ "$INSTALL_DIR" = "/" ] || [ "$INSTALL_DIR_WITHOUT_CONTROLS" != "${INSTALL_DIR}X" ]; then
    echo "Error: GORDON_INSTALL_DIR contains an unsafe control character or destination"
    exit 1
fi
case "$INSTALL_DIR" in
    *:*) echo "Error: GORDON_INSTALL_DIR must not contain ':' because it separates PATH entries"; exit 1 ;;
esac

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

install_binary_atomically() {
    binary=$1
    destination=$2
    destination_dir=${destination%/*}
    staging="${destination}.install.$$"

    if ! mkdir -p "$destination_dir" 2>/dev/null; then
        if [ -z "${GORDON_INSTALL_DIR+x}" ]; then
            echo "Error: Could not create user installation directory ${destination_dir}"
            return 1
        fi
        echo "sudo required to create ${destination_dir}"
        sudo mkdir -p "$destination_dir"
    fi

    if [ -w "$destination_dir" ]; then
        trap 'rm -f "$staging"; rm -rf "${TMP_DIR:-}"' EXIT HUP INT TERM
        install -m 0755 "$binary" "$staging"
        mv -f "$staging" "$destination"
    else
        if [ -z "${GORDON_INSTALL_DIR+x}" ]; then
            echo "Error: User installation directory is not writable: ${destination_dir}"
            return 1
        fi
        echo "sudo required to install to ${destination_dir}"
        sudo install -m 0755 "$binary" "$staging"
        if ! sudo mv -f "$staging" "$destination"; then
            sudo rm -f "$staging"
            return 1
        fi
    fi
}

path_contains_install_dir() {
    case ":${PATH:-}:" in
        *:"$INSTALL_DIR":*) return 0 ;;
        *) return 1 ;;
    esac
}

single_quote() {
    printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

print_path_instructions() {
    quoted_dir=$(single_quote "$INSTALL_DIR")
    echo "Add Gordon to the current shell with:"
    case "${SHELL##*/}" in
        fish) echo "  fish_add_path ${quoted_dir}" ;;
        *) echo "  export PATH=${quoted_dir}:\$PATH" ;;
    esac
}

update_shell_path() {
    shell_name=${SHELL##*/}
    marker_start="# >>> Gordon installer PATH >>>"
    marker_end="# <<< Gordon installer PATH <<<"

    case "$shell_name" in
        fish)
            config_file=$HOME/.config/fish/config.fish
            config_dir=${config_file%/*}
            command_line="fish_add_path $(single_quote "$INSTALL_DIR")"
            ;;
        bash)
            config_file=$HOME/.bashrc
            config_dir=$HOME
            command_line="export PATH=$(single_quote "$INSTALL_DIR"):\$PATH"
            ;;
        zsh)
            config_file=$HOME/.zshrc
            config_dir=$HOME
            command_line="export PATH=$(single_quote "$INSTALL_DIR"):\$PATH"
            ;;
        *)
            echo "Could not update PATH automatically: unsupported or missing SHELL (${SHELL:-unset})."
            print_path_instructions
            return 0
            ;;
    esac

    if ! mkdir -p "$config_dir" 2>/dev/null ||
        { [ -e "$config_file" ] && [ ! -w "$config_file" ]; } ||
        { [ ! -e "$config_file" ] && [ ! -w "$config_dir" ]; }; then
        echo "Could not update PATH automatically: ${config_file} is not writable."
        print_path_instructions
        return 0
    fi

    if [ -f "$config_file" ] && grep -F "$marker_start" "$config_file" >/dev/null 2>&1; then
        echo "PATH configuration already exists in ${config_file}."
    else
        {
            printf '\n%s\n' "$marker_start"
            printf '%s\n' "$command_line"
            printf '%s\n' "$marker_end"
        } >>"$config_file"
        echo "Added ${INSTALL_DIR} to PATH in ${config_file}."
    fi
    print_path_instructions
}

post_install() {
    echo ""
    echo "Gordon installed successfully at ${INSTALL_DIR}/gordon."

    if path_contains_install_dir; then
        "$INSTALL_DIR/gordon" version
        return
    fi

    case "${GORDON_UPDATE_PATH:-}" in
        1) update_shell_path ;;
        0) print_path_instructions ;;
        '')
            if [ -r /dev/tty ] && [ -w /dev/tty ]; then
                printf 'Add %s to your PATH configuration? [y/N] ' "$INSTALL_DIR" >/dev/tty
                if IFS= read -r answer </dev/tty; then
                    case "$answer" in
                        y|Y|yes|YES|Yes) update_shell_path ;;
                        *) print_path_instructions ;;
                    esac
                else
                    print_path_instructions
                fi
            else
                print_path_instructions
            fi
            ;;
    esac
}

version_at_least() {
    actual=$1
    required=$2
    actual_major=${actual%%.*}
    actual_minor=${actual#*.}
    actual_minor=${actual_minor%%.*}
    required_major=${required%%.*}
    required_minor=${required#*.}
    required_minor=${required_minor%%.*}

    [ "$actual_major" -gt "$required_major" ] || {
        [ "$actual_major" -eq "$required_major" ] && [ "$actual_minor" -ge "$required_minor" ]
    }
}

install_next() {
    if [ -n "${GORDON_VERSION:-}" ]; then
        echo "Error: GORDON_VERSION cannot be used with GORDON_CHANNEL=next"
        exit 1
    fi
    if [ -n "${GORDON_PRERELEASE:-}" ]; then
        echo "Error: GORDON_PRERELEASE cannot be used with GORDON_CHANNEL=next"
        exit 1
    fi
    if ! command -v go >/dev/null 2>&1; then
        echo "Error: GORDON_CHANNEL=next requires Go"
        exit 1
    fi

    echo "WARNING: UNVERIFIED DEVELOPMENT BUILD"
    echo "The next channel builds source locally from the current next branch commit."
    echo "It is not covered by the signed release checksum path and may be unstable."
    echo "Resolving the current next commit..."
    COMMIT_DATA=$(curl -fsSL -H "Accept: application/vnd.github+json" \
        "https://api.github.com/repos/${REPO}/commits/next" 2>/dev/null || echo "")
    COMMIT=$(printf '%s\n' "$COMMIT_DATA" | sed -n 's/.*"sha"[[:space:]]*:[[:space:]]*"\([0-9a-fA-F]*\)".*/\1/p' | head -n 1)
    case "$COMMIT" in
        *[!0-9a-fA-F]*|'') COMMIT="" ;;
    esac
    if [ "${#COMMIT}" -ne 40 ]; then
        echo "Error: Could not resolve an exact next commit SHA from the GitHub API"
        exit 1
    fi

    TMP_DIR=$(mktemp -d)
    trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM
    SOURCE_TARBALL="$TMP_DIR/source.tar.gz"
    SOURCE_URL="https://codeload.github.com/${REPO}/tar.gz/${COMMIT}"
    echo "Downloading source pinned to ${COMMIT}..."
    if ! curl -fsSL "$SOURCE_URL" -o "$SOURCE_TARBALL"; then
        echo "Error: Failed to download source for ${COMMIT}"
        exit 1
    fi

    SOURCE_ROOT="gordon-${COMMIT}"
    if ! tar -tzf "$SOURCE_TARBALL" >"$TMP_DIR/archive.list"; then
        echo "Error: Invalid source archive"
        exit 1
    fi
    if [ ! -s "$TMP_DIR/archive.list" ] ||
        grep -Ev "^${SOURCE_ROOT}(/.*)?$" "$TMP_DIR/archive.list" >/dev/null ||
        grep -E '(^|/)\.\.?(/|$)' "$TMP_DIR/archive.list" >/dev/null ||
        tar -tvzf "$SOURCE_TARBALL" | grep -Ev '^[-d]' >/dev/null; then
        echo "Error: Source archive contains an unsafe path or entry type"
        exit 1
    fi
    tar -xzf "$SOURCE_TARBALL" -C "$TMP_DIR"
    SOURCE_DIR="$TMP_DIR/$SOURCE_ROOT"
    if [ ! -f "$SOURCE_DIR/go.mod" ] || [ ! -f "$SOURCE_DIR/main.go" ]; then
        echo "Error: Source archive is missing go.mod or the root command"
        exit 1
    fi

    REQUIRED_GO=$(awk '$1 == "go" { print $2; exit }' "$SOURCE_DIR/go.mod")
    case "$REQUIRED_GO" in
        [0-9]*.[0-9]*) ;;
        *) echo "Error: Source go.mod has no valid Go version"; exit 1 ;;
    esac
    INSTALLED_GO=$(go env GOVERSION 2>/dev/null || true)
    INSTALLED_GO=${INSTALLED_GO#go}
    case "$INSTALLED_GO" in
        [0-9]*.[0-9]*) ;;
        *) echo "Error: Could not determine installed Go version"; exit 1 ;;
    esac
    if ! version_at_least "$INSTALLED_GO" "$REQUIRED_GO"; then
        echo "Error: Source requires Go ${REQUIRED_GO} or newer; found ${INSTALLED_GO}"
        exit 1
    fi

    BUILD_DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    NEXT_VERSION="next-${COMMIT}"
    BINARY="$TMP_DIR/gordon"
    echo "Building ${NEXT_VERSION} for ${OS}/${ARCH}..."
    if ! (cd "$SOURCE_DIR" && CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" GOTOOLCHAIN=local \
        go build -trimpath -ldflags "-s -w -X main.version=${NEXT_VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
        -o "$BINARY" .); then
        echo "Error: Failed to build Gordon from source"
        exit 1
    fi
    if [ ! -f "$BINARY" ] || [ -L "$BINARY" ]; then
        echo "Error: Build did not produce a regular Gordon binary"
        exit 1
    fi

    echo "Installing to ${INSTALL_DIR}..."
    install_binary_atomically "$BINARY" "$INSTALL_DIR/gordon"
    echo "Installed unverified next build ${COMMIT}."
}

case "$CHANNEL" in
    next)
        install_next
        post_install
        exit 0
        ;;
    stable|'') ;;
    *) echo "Error: Unsupported GORDON_CHANNEL: $CHANNEL"; exit 1 ;;
esac

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
# The sed expression is intentionally literal; shell expansion would corrupt it.
# shellcheck disable=SC2016
TARBALL_ESCAPED=$(printf '%s\n' "$TARBALL" | sed 's/[.[\*^$()+?{|]/\\&/g')
CHECKSUM_LINES=$(grep -E "^[0-9a-fA-F]{64}[[:space:]]+\*?${TARBALL_ESCAPED}\$" "$TMP_DIR/checksums.txt" || true)
CHECKSUM_COUNT=$(printf '%s\n' "$CHECKSUM_LINES" | sed '/^$/d' | wc -l | tr -d ' ')
if [ "$CHECKSUM_COUNT" != "1" ]; then
    echo "Error: Expected exactly one checksum for ${TARBALL} in checksums.txt"
    exit 1
fi
EXPECTED_CHECKSUM=$(printf '%s\n' "$CHECKSUM_LINES" | awk '{print $1}')

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

echo "Extracting Gordon binary..."
EXTRACT_DIR="$TMP_DIR/extract"
mkdir -p "$EXTRACT_DIR"
tar -xzf "$TMP_DIR/$TARBALL" -C "$EXTRACT_DIR" gordon

BINARY="$EXTRACT_DIR/gordon"
if [ ! -f "$BINARY" ] || [ -L "$BINARY" ]; then
    echo "Error: Archive did not contain a regular Gordon binary"
    exit 1
fi

echo "Installing to ${INSTALL_DIR}..."
install_binary_atomically "$BINARY" "$INSTALL_DIR/gordon"

post_install
