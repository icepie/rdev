#!/usr/bin/env python3
import os
import shutil
import sys
from pathlib import Path


def split_paths(value):
    if not value:
        return []
    return [Path(item) for item in value.split(os.pathsep) if item]


def candidates():
    env_files = ["RDEV_WINPTY_DLL", "WINPTY_DLL"]
    env_dirs = ["RDEV_WINPTY_DIR", "WINPTY_DIR"]
    for name in env_files:
        value = os.environ.get(name)
        if value:
            yield Path(value)
    for name in env_dirs:
        for root in split_paths(os.environ.get(name)):
            yield root / "winpty.dll"

    repo = Path(__file__).resolve().parents[3]
    roots = [
        repo / "third_party" / "winpty" / "x64",
        repo / "clients" / "rdev-client-gpu" / "win7" / "winpty" / "x64",
        Path("/usr/lib/node_modules/@google/gemini-cli/node_modules/node-pty/prebuilds/win32-x64"),
        Path("/data/Projects/pi-fit/node_modules/node-pty/prebuilds/win32-x64"),
        Path("/data/Projects/pi-gui/apps/desktop/resources/git-bash/runtime/x64/usr/bin"),
        Path("/data/Projects/pi-fit/apps/desktop/resources/git-bash/runtime/x64/usr/bin"),
    ]
    for root in roots:
        yield root / "winpty.dll"

    for path_item in split_paths(os.environ.get("PATH")):
        yield path_item / "winpty.dll"


def main():
    if len(sys.argv) != 2:
        print("usage: copy_winpty_runtime.py <dist-dir>", file=sys.stderr)
        return 2
    dist = Path(sys.argv[1])
    dist.mkdir(parents=True, exist_ok=True)

    seen = set()
    for dll in candidates():
        dll = dll.expanduser()
        key = str(dll)
        if key in seen:
            continue
        seen.add(key)
        agent = dll.with_name("winpty-agent.exe")
        if dll.is_file() and agent.is_file():
            shutil.copy2(dll, dist / "winpty.dll")
            shutil.copy2(agent, dist / "winpty-agent.exe")
            print(f"copied WinPTY runtime from {dll.parent}")
            return 0

    print(
        "warning: WinPTY runtime not found; Win7/Win8 PTY will fall back to pipe shell. "
        "Set RDEV_WINPTY_DIR or WINPTY_DIR to a directory containing winpty.dll and winpty-agent.exe.",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
