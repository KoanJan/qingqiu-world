#!/bin/bash
# generate-icons.sh — Generate all icon assets from a source favicon (SVG or PNG).
#
# Derivation logic:
#   SVG source: embed in 1024×1024 rounded-corner SVG → rasterize to PNG sizes.
#   PNG source: resize directly to each target size with rounded corners (Pillow).
#
#   Then: package PNGs into .ico (Windows) and .icns (macOS) formats.
#
# Prerequisites:
#   - rsvg-convert  (brew install librsvg)  [SVG only]
#   - iconutil      (macOS built-in)
#   - python3       (macOS built-in, requires Pillow)
#
# Usage:
#   ./app-icons-builder/generate-icons.sh                         # default: web/public/favicon.svg
#   ./app-icons-builder/generate-icons.sh -i path/to/icon.png     # specify custom source

set -euo pipefail
cd "$(dirname "$0")/.."

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
INPUT=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -i|--input)
            INPUT="$2"
            shift 2
            ;;
        *)
            echo "Usage: $0 [-i|--input <source.svg|source.png>]"
            exit 1
            ;;
    esac
done

# Default to web favicon
INPUT="${INPUT:-web/public/favicon.svg}"

if [[ ! -f "$INPUT" ]]; then
    echo "ERROR: source file not found: $INPUT"
    exit 1
fi

CANVAS=1024
CORNER_RADIUS=224
# Source content is scaled to CONTENT_SCALE of the canvas and centred,
# leaving visual padding so the icon doesn't appear larger than other apps.
CONTENT_SCALE=0.85
BUILD_DIR="app-icons"
ICONSET="${BUILD_DIR}/icon.iconset"
SCRIPT_DIR="app-icons-builder"
SIZES=(16 32 48 64 128 256 512 1024)

mkdir -p "$BUILD_DIR"

# Infer type from extension
EXT="${INPUT##*.}"
EXT_LOWER=$(echo "$EXT" | tr '[:upper:]' '[:lower:]')

# ---------------------------------------------------------------------------
# PNG path: resize + rounded corners with Pillow
# ---------------------------------------------------------------------------
if [[ "$EXT_LOWER" == "png" ]]; then
    echo "==> PNG source detected: $INPUT"
    echo "==> Generating rounded-corner PNGs directly..."

    # Build comma-separated size list for Python
    SIZES_CSV=$(IFS=, ; echo "${SIZES[*]}")

    python3 -c "
from PIL import Image, ImageDraw

src = Image.open('${INPUT}').convert('RGBA')
canvas = ${CANVAS}
content_scale = ${CONTENT_SCALE}
content_size = round(canvas * content_scale)
offset = (canvas - content_size) // 2
sizes = [${SIZES_CSV}]

# Source corner radius at original (1254px), scaled to content_size.
# The original favicon_v3.png has rounded corners ~220px at 1254px.
src_corner_r = round(220 * content_size / src.height)

# Composite onto white background to prevent dark fringing during RGBA resize.
# Pillow's LANCZOS corrupts transparent RGB values — compositing first
# ensures all pixels have proper RGB, then our own mask handles transparency.
bg_white = Image.new('RGBA', (src.width, src.height), (255, 255, 255, 255))
bg_white.paste(src, (0, 0), src)
src = bg_white

# Scale source to content_size for visual padding
src = src.resize((content_size, content_size), Image.LANCZOS)

# Paste centred onto transparent canvas
canvas_img = Image.new('RGBA', (canvas, canvas), (255, 255, 255, 0))
canvas_img.paste(src, (offset, offset))

# Build mask positioned at source content area, not full canvas.
# This ensures areas outside the source content stay transparent.
mask = Image.new('L', (canvas, canvas), 0)
draw = ImageDraw.Draw(mask)
draw.rounded_rectangle(
    [(offset, offset), (offset + content_size - 1, offset + content_size - 1)],
    radius=src_corner_r,
    fill=255
)
canvas_img.putalpha(mask)

for size in sizes:
    icon = canvas_img.resize((size, size), Image.LANCZOS)
    # Scale mask parameters to target size
    s_off = round(offset * size / canvas)
    s_cs = round(content_size * size / canvas)
    s_r = round(src_corner_r * size / canvas)
    m = Image.new('L', (size, size), 0)
    draw = ImageDraw.Draw(m)
    draw.rounded_rectangle(
        [(s_off, s_off), (s_off + s_cs - 1, s_off + s_cs - 1)],
        radius=s_r,
        fill=255
    )
    icon.putalpha(m)
    out_path = '${BUILD_DIR}/icon-' + str(size) + '.png'
    icon.save(out_path, 'PNG')
    print(f'    {size}×{size} → {out_path}')
" 2>&1

    echo "    → PNGs generated directly, skipped icon-source.svg"

# ---------------------------------------------------------------------------
# SVG path: embed in wrapper, then rasterize
# ---------------------------------------------------------------------------
else
    echo "==> SVG source detected: $INPUT"
    echo "==> Generating icon-source.svg..."

    SOURCE="${BUILD_DIR}/icon-source.svg"

    INNER=$(python3 -c "
import re
with open('$INPUT', 'r') as f:
    content = f.read()
content = re.sub(r'<\?xml[^?]*\?>', '', content)
content = re.sub(r'<!DOCTYPE[^>]*>', '', content)
tag_match = re.search(r'<svg\s+([^>]*)>', content, re.DOTALL)
tag_attrs = tag_match.group(1) if tag_match else ''
vb_match = re.search(r'viewBox\s*=\s*\"([^\"]+)\"', tag_attrs)
viewbox = vb_match.group(1) if vb_match else '0 0 ${CANVAS} ${CANVAS}'
content = re.sub(
    r'<svg\s+[^>]*>',
    '<svg version=\"1.0\" xmlns=\"http://www.w3.org/2000/svg\"'
    ' x=\"0\" y=\"0\"'
    ' width=\"${CANVAS}\" height=\"${CANVAS}\"'
    ' viewBox=\"' + viewbox + '\"'
    ' preserveAspectRatio=\"xMidYMid slice\">',
    content,
    count=1,
    flags=re.DOTALL
)
print(content.strip())
")

    cat > "$SOURCE" << SVGEOF
<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg"
 width="${CANVAS}" height="${CANVAS}" viewBox="0 0 ${CANVAS} ${CANVAS}">

<defs>
  <clipPath id="rounded-corners">
    <rect x="0" y="0" width="${CANVAS}" height="${CANVAS}" rx="${CORNER_RADIUS}" ry="${CORNER_RADIUS}"/>
  </clipPath>
</defs>

<g clip-path="url(#rounded-corners)">
  <g transform="translate($(python3 -c "print(round(${CANVAS} * (1 - ${CONTENT_SCALE}) / 2))"), $(python3 -c "print(round(${CANVAS} * (1 - ${CONTENT_SCALE}) / 2))")) scale(${CONTENT_SCALE})">
${INNER}
  </g>
</g>
</svg>
SVGEOF

    echo "    → $SOURCE"
    echo "==> Rasterizing PNGs..."

    for SIZE in "${SIZES[@]}"; do
        OUTPUT="${BUILD_DIR}/icon-${SIZE}.png"
        rsvg-convert -w "$SIZE" -h "$SIZE" -o "$OUTPUT" "$SOURCE"
        echo "    ${SIZE}×${SIZE} → ${OUTPUT}"
    done
fi

# ---------------------------------------------------------------------------
# Package: macOS .icns
# ---------------------------------------------------------------------------
echo "==> Generating icon.icns..."

rm -rf "$ICONSET"
mkdir -p "$ICONSET"

cp "${BUILD_DIR}/icon-16.png"   "$ICONSET/icon_16x16.png"
cp "${BUILD_DIR}/icon-32.png"   "$ICONSET/icon_16x16@2x.png"
cp "${BUILD_DIR}/icon-32.png"   "$ICONSET/icon_32x32.png"
cp "${BUILD_DIR}/icon-64.png"   "$ICONSET/icon_32x32@2x.png"
cp "${BUILD_DIR}/icon-128.png"  "$ICONSET/icon_128x128.png"
cp "${BUILD_DIR}/icon-256.png"  "$ICONSET/icon_128x128@2x.png"
cp "${BUILD_DIR}/icon-256.png"  "$ICONSET/icon_256x256.png"
cp "${BUILD_DIR}/icon-512.png"  "$ICONSET/icon_256x256@2x.png"
cp "${BUILD_DIR}/icon-512.png"  "$ICONSET/icon_512x512.png"
cp "${BUILD_DIR}/icon-1024.png" "$ICONSET/icon_512x512@2x.png"

iconutil -c icns "$ICONSET" -o "${BUILD_DIR}/icon.icns"
echo "    → ${BUILD_DIR}/icon.icns"

# ---------------------------------------------------------------------------
# Package: Windows .ico
# ---------------------------------------------------------------------------
echo "==> Generating icon.ico..."

python3 "${SCRIPT_DIR}/make-ico.py" \
    "${BUILD_DIR}/icon-16.png" \
    "${BUILD_DIR}/icon-32.png" \
    "${BUILD_DIR}/icon-48.png" \
    "${BUILD_DIR}/icon-64.png" \
    "${BUILD_DIR}/icon-128.png" \
    "${BUILD_DIR}/icon-256.png" \
    -o "${BUILD_DIR}/icon.ico"

echo "    → ${BUILD_DIR}/icon.ico"

echo "==> Done. All icon assets generated in ${BUILD_DIR}/"
