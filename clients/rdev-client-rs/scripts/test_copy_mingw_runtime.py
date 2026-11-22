#!/usr/bin/env python3
from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from copy_mingw_runtime import REQUIRED_DLLS, copy_runtime


class CopyMingwRuntimeTest(unittest.TestCase):
    def test_copies_all_required_dlls_from_nested_toolchain_paths(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            gcc = root / "usr" / "lib" / "gcc" / "x86_64-w64-mingw32" / "14-posix"
            target_bin = root / "usr" / "x86_64-w64-mingw32" / "bin"
            dist = root / "dist"
            for name in REQUIRED_DLLS[:2]:
                path = gcc / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(name.encode())
            target_bin.mkdir(parents=True)
            (target_bin / REQUIRED_DLLS[2]).write_bytes(REQUIRED_DLLS[2].encode())

            copied = copy_runtime(dist, [root / "usr" / "x86_64-w64-mingw32", root / "usr" / "lib" / "gcc"])

            self.assertEqual([path.name for path in copied], list(REQUIRED_DLLS))
            for name in REQUIRED_DLLS:
                self.assertEqual((dist / name).read_bytes(), name.encode())

    def test_copies_explicit_llvm_runtime_names(self) -> None:
        names = ("libc++.dll", "libunwind.dll", "libwinpthread-1.dll")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "llvm-mingw" / "bin"
            source.mkdir(parents=True)
            for name in names:
                (source / name).write_bytes(name.encode())

            copied = copy_runtime(root / "dist", [root / "llvm-mingw"], names)

            self.assertEqual([path.name for path in copied], list(names))
            for name in names:
                self.assertEqual((root / "dist" / name).read_bytes(), name.encode())

    def test_fails_when_a_required_dll_is_missing(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for name in REQUIRED_DLLS[:2]:
                path = root / name
                path.write_bytes(name.encode())

            with self.assertRaisesRegex(FileNotFoundError, REQUIRED_DLLS[2]):
                copy_runtime(root / "dist", [root])


if __name__ == "__main__":
    unittest.main()
