#!/usr/bin/env python3
import argparse
import os
import subprocess
import tempfile
from pathlib import Path


def supports_mcrtdll(gcc: str) -> bool:
    with tempfile.TemporaryDirectory() as tmp:
        src = Path(tmp) / "probe.c"
        out = Path(tmp) / "probe.dll"
        src.write_text("int probe(void) { return 0; }\n", encoding="utf-8")
        proc = subprocess.run(
            [gcc, "-shared", "-Os", "-mcrtdll=msvcrt-os", "-o", str(out), str(src)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return proc.returncode == 0


def build(gcc: str, source: Path, output: Path, extra: list[str], use_mcrtdll: bool) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    cmd = [gcc, "-shared", "-Os"]
    if use_mcrtdll:
        cmd.append("-mcrtdll=msvcrt-os")
    cmd += ["-o", str(output), str(source)] + extra
    subprocess.run(cmd, check=True)


def main() -> None:
    parser = argparse.ArgumentParser(description="Build Win7 compatibility shim DLLs.")
    parser.add_argument("out_dir")
    parser.add_argument("--gcc", default=os.environ.get("MINGW_GCC", "x86_64-w64-mingw32-gcc"))
    args = parser.parse_args()

    root = Path(__file__).resolve().parent
    out_dir = Path(args.out_dir)
    default_crt = os.environ.get("RDEV_MINGW_CRT") == "msvcrt"
    use_mcrtdll = False if default_crt else supports_mcrtdll(args.gcc)
    if default_crt:
        print("building Win7 shims with toolchain default MSVCRT")
    elif use_mcrtdll:
        print("building Win7 shims with -mcrtdll=msvcrt-os")
    else:
        print("building Win7 shims without -mcrtdll=msvcrt-os (unsupported by this MinGW)")

    build(args.gcc, root / "waitonaddress_shim.c", out_dir / "rdev-waitonaddress-shim.dll", [], use_mcrtdll)
    build(args.gcc, root / "bcprng_shim.c", out_dir / "rdev-bcprng.dll", [], use_mcrtdll)
    build(args.gcc, root / "ws2_32_shim.c", out_dir / "rdevws.dll", ["-lws2_32"], use_mcrtdll)


if __name__ == "__main__":
    main()
