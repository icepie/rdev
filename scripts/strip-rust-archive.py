#!/usr/bin/env python3
"""Remove Rust LLVM sections without passing COFF short imports to objcopy."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def strip_archive(archive, objcopy, ar):
    archive = Path(archive)
    data = archive.read_bytes()
    if not data.startswith(b"!<arch>\n"):
        raise ValueError("expected a regular ar archive")
    with tempfile.TemporaryDirectory(dir=archive.parent) as directory:
        root = Path(directory)
        rewritten = root / archive.name
        with rewritten.open("wb") as output:
            output.write(data[:8])
            offset = 8
            while offset < len(data):
                header = data[offset:offset + 60]
                if len(header) != 60 or header[58:] != b"`\n":
                    raise ValueError("invalid archive member header")
                size = int(header[48:58])
                body = data[offset + 60:offset + 60 + size]
                if len(body) != size:
                    raise ValueError("truncated archive member")
                offset += 60 + size + size % 2
                name = header[:16].strip()
                if name in (b"/", b"/SYM64/"):
                    continue  # llvm-ar regenerates symbol offsets below.
                # Short COFF import records have version 0, unlike bigobj v2.
                short_import = body.startswith(b"\x00\x00\xff\xff\x00\x00")
                if name != b"//" and not short_import:
                    source, target = root / "member.o", root / "stripped.o"
                    source.write_bytes(body)
                    subprocess.run([objcopy, "--remove-section=.llvmbc",
                                    "--remove-section=.llvmcmd", str(source), str(target)], check=True)
                    body = target.read_bytes()
                output.write(header[:48] + str(len(body)).encode().ljust(10) + header[58:])
                output.write(body)
                if len(body) % 2:
                    output.write(b"\n")
        subprocess.run([ar, "s", str(rewritten)], check=True)
        os.chmod(rewritten, archive.stat().st_mode)
        os.replace(rewritten, archive)


if __name__ == "__main__":
    strip_archive(*sys.argv[1:])
