#!/usr/bin/env python3
"""Patch selected PE import strings for Windows 7 packaging.

This keeps the normal Rust Windows GNU build but rewrites known Win8+ import
names to Win7-compatible symbols or bundled shim DLLs. Replacements must be no
longer than the original import strings so the PE layout stays unchanged.
"""

from __future__ import annotations

import argparse
from pathlib import Path

REPLACEMENTS = [
    (b"GetSystemTimePreciseAsFileTime", b"GetSystemTimeAsFileTime"),
    (b"api-ms-win-core-synch-l1-2-0.dll", b"rdev-waitonaddress-shim.dll"),
    (b"bcryptprimitives.dll", b"rdev-bcprng.dll"),
    (b"WS2_32.dll", b"rdevws.dll"),
]


def padded(new: bytes, old: bytes) -> bytes:
    if len(new) > len(old):
        raise ValueError(f"replacement {new!r} is longer than {old!r}")
    return new + (b"\0" * (len(old) - len(new)))


def patch_imports(data: bytes) -> tuple[bytes, list[tuple[bytes, int]]]:
    counts: list[tuple[bytes, int]] = []
    for old, new in REPLACEMENTS:
        count = data.count(old)
        if count:
            data = data.replace(old, padded(new, old))
        counts.append((old, count))
    return data, counts


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()

    data = args.input.read_bytes()
    patched, counts = patch_imports(data)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(patched)
    for old, count in counts:
        print(f"{old.decode(errors='replace')}: {count}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
