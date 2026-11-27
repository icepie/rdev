#!/usr/bin/env bash
# Build one rdev Go binary with the repository's release flags and an explicit
# per-platform compression policy.
#
#   build-go.sh <package> <output> <upx|none> [extra ldflags]
#
# RDEV_LDFLAGS is the shared linker flag set (CI sets -s -w -X main.version=…);
# extra flags such as -checklinkname=0 are appended for the platforms that
# need them. GOOS/GOARCH/CGO_ENABLED/CC/GOROOT and PATH are inherited, so the
# go-win7 and Android toolchains keep working unchanged.
#
# Compression policy is a caller decision, not a default: standard and Windows
# releases keep UPX, Android never gets UPX (it strips PT_INTERP and Android 10+
# refuses the self-decompressing stub). A missing or failing upx degrades to the
# uncompressed binary, matching the previous `|| cp` behaviour.
set -euo pipefail

package=${1:?usage: build-go.sh <package> <output> <upx|none> [extra ldflags]}
output=${2:?usage: build-go.sh <package> <output> <upx|none> [extra ldflags]}
compress=${3:?usage: build-go.sh <package> <output> <upx|none> [extra ldflags]}
extra=${4:-}

: "${RDEV_LDFLAGS:=-s -w}"
go_bin=${GO:-go}

mkdir -p "$(dirname "$output")"
staging="${output}.unstripped"
rm -f "$staging" "$output"

# shellcheck disable=SC2086 # the flag set is intentionally word-split
"$go_bin" build -trimpath -ldflags "$RDEV_LDFLAGS $extra" -o "$staging" "$package"

case "$compress" in
  upx)
    if command -v upx >/dev/null 2>&1 && upx --best "$staging" -o "$output"; then
      rm -f "$staging"
    else
      echo "upx unavailable or failed; shipping $output uncompressed" >&2
      mv "$staging" "$output"
    fi
    ;;
  none)
    mv "$staging" "$output"
    ;;
  *)
    echo "unknown compression policy: $compress (use upx or none)" >&2
    exit 2
    ;;
esac

ls -l "$output"
