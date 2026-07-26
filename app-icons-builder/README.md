# App Icons Builder

Generate all platform icon assets from a single source favicon.

## Prerequisites

| Tool | Install | Required For |
|------|---------|-------------|
| Pillow | `pip install Pillow` | Always |
| rsvg-convert | `brew install librsvg` | SVG source only |
| iconutil | macOS built-in | ICNS packaging |

## Usage

```bash
# Default: use web/public/favicon.svg
./app-icons-builder/generate-icons.sh

# Specify a custom source (SVG or PNG)
./app-icons-builder/generate-icons.sh -i path/to/icon.png
```

## Source Types

| Extension | Processing |
|-----------|-----------|
| `.svg` | Embed in rounded-corner wrapper SVG → rasterize with rsvg-convert |
| `.png` | Resize + rounded corners directly with Pillow |

## Output

All outputs go to `app-icons/`:

```
app-icons/
├── icon-16.png         ... icon-1024.png   (8 raster sizes)
├── icon.iconset/                           (macOS iconset directory)
├── icon.icns                               (macOS app icon)
├── icon.ico                                (Windows app icon)
└── icon-source.svg                         (SVG only: master icon template)
```

Referenced by `electron-builder.yml` for packaging.
