#!/bin/bash
# Build all platform packages at once.
#
# Run from the project root. Designed for macOS as the build host.
#
# Outputs:
#   dist-electron/mac/amd64/    — macOS Intel (.dmg)
#   dist-electron/mac/arm64/    — macOS Apple Silicon (.dmg)
#   dist-electron/win/amd64/    — Windows amd64 (.exe installer)
#   dist-electron/linux/amd64/  — Linux amd64 (.deb / .rpm)
#   dist-electron/linux/arm64/  — Linux arm64 (.deb / .rpm)
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

START_TIME=$(date +%s)

echo "========================================="
echo "  BUILD ALL PLATFORMS"
echo "  Started at $(date '+%Y-%m-%d %H:%M:%S')"
echo "========================================="

# Step 0: Build web and electron shared artifacts once
echo ""
echo ">>> Building shared web + electron artifacts"
npm run build

# Step 1: macOS amd64
echo ""
echo "========================================="
echo "  [1/5] macOS amd64"
echo "========================================="
rm -f server/qingqiu-world-server server/qingqiu-world-server.exe
npm run dist:mac:amd64

# Step 2: macOS arm64
echo ""
echo "========================================="
echo "  [2/5] macOS arm64"
echo "========================================="
rm -f server/qingqiu-world-server server/qingqiu-world-server.exe
npm run dist:mac:arm64

# Step 3: Windows amd64
echo ""
echo "========================================="
echo "  [3/5] Windows amd64"
echo "========================================="
rm -f server/qingqiu-world-server server/qingqiu-world-server.exe
npm run dist:win

# Step 4: Linux (both amd64 and arm64)
echo ""
echo "========================================="
echo "  [4/4] Linux (amd64 + arm64)"
echo "========================================="
rm -f server/qingqiu-world-server server/qingqiu-world-server.exe
./dist-linux-on-mac.sh all

# Summary
END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo "========================================="
echo "  BUILD COMPLETE"
echo "  Finished at $(date '+%Y-%m-%d %H:%M:%S')"
echo "  Duration: $((ELAPSED / 60))m $((ELAPSED % 60))s"
echo "========================================="
echo ""
echo "Output packages:"
find dist-electron -name "*.dmg" -o -name "*.deb" -o -name "*.rpm" -o -name "*.exe" 2>/dev/null | while read -r f; do
    echo "  $f  ($(du -sh "$f" | cut -f1))"
done
echo ""
