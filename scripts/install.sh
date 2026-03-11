#!/bin/sh
set -e

# op-forward installer
# Usage: curl -fsSL https://raw.githubusercontent.com/ekovshilovsky/op-forward/main/scripts/install.sh | sh

REPO="ekovshilovsky/op-forward"
INSTALL_DIR="${OP_FORWARD_INSTALL_DIR:-$HOME/.local/bin}"

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
    esac

    case "$OS" in
        darwin|linux) ;;
        *) echo "Unsupported OS: $OS"; exit 1 ;;
    esac

    echo "${OS}_${ARCH}"
}

get_latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | \
        grep '"tag_name"' | head -1 | cut -d'"' -f4
}

main() {
    PLATFORM=$(detect_platform)
    VERSION=$(get_latest_version)

    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version"
        exit 1
    fi

    echo "Installing op-forward ${VERSION} for ${PLATFORM}..."

    ARCHIVE_NAME="op-forward_${VERSION#v}_${PLATFORM}.tar.gz"
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE_NAME}"

    TMP_DIR=$(mktemp -d)
    trap 'rm -rf "$TMP_DIR"' EXIT

    echo "Downloading ${DOWNLOAD_URL}..."
    curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${ARCHIVE_NAME}"

    echo "Extracting..."
    tar -xzf "${TMP_DIR}/${ARCHIVE_NAME}" -C "$TMP_DIR"

    mkdir -p "$INSTALL_DIR"
    cp "${TMP_DIR}/op-forward" "${INSTALL_DIR}/op-forward"
    chmod +x "${INSTALL_DIR}/op-forward"

    echo ""
    echo "op-forward ${VERSION} installed to ${INSTALL_DIR}/op-forward"

    if ! echo "$PATH" | tr ':' '\n' | grep -q "^${INSTALL_DIR}$"; then
        echo ""
        echo "Add to your PATH: export PATH=\"${INSTALL_DIR}:\$PATH\""
    fi

    echo ""
    echo "Quick start:"
    echo "  op-forward serve              # Start the daemon"
    echo "  op-forward service install    # Install as launchd service (macOS)"
}

main
