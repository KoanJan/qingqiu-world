#!/usr/bin/env python3
"""
make-ico.py — Package one or more PNG images into a Windows .ico file.

Pure Python, zero dependencies.  Each input PNG is embedded as-is (modern
ICO format uses PNG data directly, not BMP).

Usage:
    python3 make-ico.py icon-16.png icon-32.png icon-48.png -o icon.ico
"""

import struct
import sys
import argparse
from pathlib import Path


def build_ico(png_paths: list[str], output_path: str) -> None:
    """Read PNG files and write a multi-resolution ICO file."""
    entries: list[tuple[int, int, bytes]] = []  # (width, height, png_data)

    for path in png_paths:
        data = Path(path).read_bytes()

        # Parse IHDR chunk to get actual dimensions
        # PNG signature (8) + IHDR length (4) + "IHDR" (4) + width (4) + height (4)
        if data[:8] != b'\x89PNG\r\n\x1a\n':
            print(f"WARNING: {path} is not a valid PNG, skipping", file=sys.stderr)
            continue

        width = struct.unpack('>I', data[16:20])[0]
        height = struct.unpack('>I', data[20:24])[0]

        # ICO stores dimensions as 1-byte each; 256 → 0
        w_byte = width if width < 256 else 0
        h_byte = height if height < 256 else 0

        entries.append((w_byte, h_byte, data))

    if not entries:
        print("ERROR: no valid PNG files provided", file=sys.stderr)
        sys.exit(1)

    # Sort by size descending (largest first is conventional)
    entries.sort(key=lambda e: len(e[2]), reverse=True)

    # Compute file layout
    header_size = 6  # reserved(2) + type(2) + count(2)
    dir_entry_size = 16
    dir_size = header_size + len(entries) * dir_entry_size

    offsets: list[int] = []
    offset = dir_size
    for _, _, data in entries:
        offsets.append(offset)
        offset += len(data)

    # Write
    with open(output_path, 'wb') as f:
        # ICO header
        f.write(struct.pack('<HHH', 0, 1, len(entries)))

        # Directory entries
        for (w, h, data), off in zip(entries, offsets):
            size = len(data)
            f.write(struct.pack('<BBBBHHII',
                w,       # width  (0 = 256)
                h,       # height (0 = 256)
                0,       # palette colours
                0,       # reserved
                1,       # colour planes
                32,      # bits per pixel
                size,    # image size
                off,     # offset to image data
            ))

        # Image data (raw PNG)
        for _, _, data in entries:
            f.write(data)

    print(f"Written {output_path} ({len(entries)} resolutions)")


def main() -> None:
    parser = argparse.ArgumentParser(description="Package PNGs into a Windows .ico file")
    parser.add_argument('pngs', nargs='+', help='PNG files to include')
    parser.add_argument('-o', '--output', required=True, help='Output .ico file path')
    args = parser.parse_args()
    build_ico(args.pngs, args.output)


if __name__ == '__main__':
    main()
