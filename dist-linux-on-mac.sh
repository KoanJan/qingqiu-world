#!/bin/bash
# Build Linux distribution with workaround for macOS provenance xattr issue.
#
# Usage: ./dist-linux-on-mac.sh [amd64|arm64|all]
#   amd64 — x86_64 (most Linux servers)  [default]
#   arm64 — aarch64 (AWS Graviton, Apple Silicon Linux VMs)
#   all   — build both amd64 and arm64
#
# All components (Go server including embedded bwrap, Electron runtime)
# are built for the same target architecture.
#
# On macOS, the com.apple.provenance extended attribute prevents app-builder
# from extracting the `electron` ELF binary from the cached zip. This script
# uses electron-builder's --dir flag to unpack first, patches the missing
# binary, then runs the final packaging step.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Parse architecture list
MODE="${1:-amd64}"
ARCH_LIST=()

case "$MODE" in
    amd64|x86_64)  ARCH_LIST=("amd64")  ;;
    arm64|aarch64) ARCH_LIST=("arm64")  ;;
    all)           ARCH_LIST=("amd64" "arm64") ;;
    *)
        echo "Unsupported architecture: $MODE (use amd64, arm64, or all)"
        exit 1
        ;;
esac

build_arch() {
    local ARCH="$1"
    local GOARCH ELECTRON_ARCH

    case "$ARCH" in
        amd64)  GOARCH=amd64;  ELECTRON_ARCH=x64   ;;
        arm64)  GOARCH=arm64;  ELECTRON_ARCH=arm64 ;;
    esac

    ELECTRON_ZIP="$HOME/Library/Caches/electron/electron-v35.7.5-linux-${ELECTRON_ARCH}.zip"
    OUTPUT_DIR="dist-electron/linux/${GOARCH}"
    UNPACKED_DIR="$OUTPUT_DIR/linux-unpacked"

    # Step 1: Build Go server for Linux (embed bwrap for $GOARCH via build tags)
    echo "=== Building Go server for linux/$GOARCH ==="
    rm -f server/qingqiu-world-server
    cd server && CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -o qingqiu-world-server ./cmd/
    cd "$SCRIPT_DIR"

    # Step 2: Build web and electron
    echo "=== Building web and electron ==="
    npm run build

    # Step 3: Clean previous output
    rm -rf "$OUTPUT_DIR"

    # Step 4: Unpack only (no packaging) - will fail on rename, but creates the directory
    echo "=== Unpacking electron (expected to fail on rename) ==="
    npx electron-builder --linux --"$ELECTRON_ARCH" --dir --config.directories.output="$OUTPUT_DIR" || true

    # Step 5: Normalize unpacked directory name (electron-builder uses linux-arm64-unpacked for arm64)
    if [ -d "$OUTPUT_DIR/linux-arm64-unpacked" ]; then
        mv "$OUTPUT_DIR/linux-arm64-unpacked" "$UNPACKED_DIR"
    fi

    # Step 6: Patch the missing electron binary
    if [ ! -f "$UNPACKED_DIR/qingqiu-world" ] && [ -f "$ELECTRON_ZIP" ]; then
        echo "=== Patching missing electron binary ==="
        cd "$UNPACKED_DIR"
        unzip -o "$ELECTRON_ZIP" electron
        mv electron qingqiu-world
        cd "$SCRIPT_DIR"
        echo "=== Patched successfully ==="
    else
        echo "=== qingqiu-world binary already exists or zip not found, skipping patch ==="
    fi

    # Step 7: Package the pre-built directory into deb/rpm
    echo "=== Packaging Linux distribution (deb + rpm) ==="
    npx electron-builder --linux --"$ELECTRON_ARCH" --prepackaged "$UNPACKED_DIR" --config.directories.output="$OUTPUT_DIR"

    echo "=== Linux distribution build complete (linux/$GOARCH) ==="
    echo ""
}

for ARCH in "${ARCH_LIST[@]}"; do
    build_arch "$ARCH"
done

echo "=== All Linux builds complete ==="
