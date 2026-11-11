#!/usr/bin/env python3
"""Copy required MinGW runtime DLLs into a Windows release directory."""

from __future__ import annotations

import argparse
import shutil
from pathlib import Path

REQUIRED_DLLS = (
    "libgcc_s_seh-1.dll",
    "libstdc++-6.dll",
    "libwinpthread-1.dll",
)


def find_runtime_dll(name: str, roots: list[Path]) -> Path:
    for root in roots:
        for candidate in (root / name, root / "bin" / name, root / "lib" / name):
            if candidate.is_file():
                return candidate
        if root.is_dir():
            for candidate in sorted(root.rglob(name)):
                if candidate.is_file():
                    return candidate
    searched = ", ".join(str(root) for root in roots)
    raise FileNotFoundError(f"required MinGW runtime {name} not found under: {searched}")


def copy_runtime(dist: Path, roots: list[Path], names: tuple[str, ...] = REQUIRED_DLLS) -> list[Path]:
    dist.mkdir(parents=True, exist_ok=True)
    copied = []
    for name in names:
        source = find_runtime_dll(name, roots)
        target = dist / name
        shutil.copy2(source, target)
        copied.append(target)
        print(f"copied {name} from {source}")
    return copied


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, required=True)
    parser.add_argument("--search-root", type=Path, action="append", required=True)
    parser.add_argument("--dll", dest="names", action="append")
    args = parser.parse_args()
    names = tuple(args.names) if args.names else REQUIRED_DLLS
    copy_runtime(args.dist_dir, args.search_root, names)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
