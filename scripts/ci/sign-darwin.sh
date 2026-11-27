#!/usr/bin/env bash
# Sign, verify and notarize macOS payloads with the Developer ID identity set up
# by scripts/ci/apple-signing-setup.sh.
#
#   sign-darwin.sh <path>...
#
# A path is a file (signed as-is) or a directory (every Mach-O file inside is
# signed, deepest first so dylibs and frameworks carry signatures before the
# executables that link them). Everything signed is then notarized: archives are
# submitted as they are, directories as a zip built with ditto.
#
# Notarization of a bare Mach-O has no stapled ticket; the browser downloads of
# rdev are plain binaries, so Gatekeeper validates them online with the same
# notarization record.
set -euo pipefail

if ! command -v codesign >/dev/null 2>&1 || ! command -v xcrun >/dev/null 2>&1; then
  echo "::error::codesign and xcrun are required (run this on macOS)" >&2
  exit 1
fi

identity="${APPLE_SIGNING_IDENTITY:?APPLE_SIGNING_IDENTITY is required}"
profile="${APPLE_NOTARY_PROFILE:?APPLE_NOTARY_PROFILE is required}"
keychain="${APPLE_NOTARY_KEYCHAIN:?APPLE_NOTARY_KEYCHAIN is required}"

if [ "$#" -eq 0 ]; then
  echo "usage: sign-darwin.sh <path>..." >&2
  exit 2
fi

is_macho() {
  file -b "$1" 2>/dev/null | grep -q 'Mach-O'
}

# Mach-O files under the given paths, deepest paths first.
collect_macho() {
  for path in "$@"; do
    if [ -d "$path" ]; then
      find "$path" -type f -print | awk '{ print length($0) "\t" $0 }' | sort -rn | cut -f2- |
        while IFS= read -r candidate; do
          if is_macho "$candidate"; then printf '%s\n' "$candidate"; fi
        done
    elif is_macho "$path"; then
      printf '%s\n' "$path"
    fi
  done
}

mapfile_targets=()
while IFS= read -r file; do
  mapfile_targets+=("$file")
done < <(collect_macho "$@")

if [ "${#mapfile_targets[@]}" -eq 0 ]; then
  echo "::error::no Mach-O payload found to sign" >&2
  exit 1
fi

for file in "${mapfile_targets[@]}"; do
  codesign --force --timestamp --options runtime --keychain "$keychain" --sign "$identity" "$file"
  codesign --verify --strict --verbose=2 "$file"
  authority="$(codesign -dv --verbose=4 "$file" 2>&1 | sed -n 's/^Authority=//p' | head -n 1)"
  case "$authority" in
    "Developer ID Application:"*) ;;
    *) echo "::error::$file is not signed by a Developer ID Application certificate ($authority)" >&2; exit 1 ;;
  esac
  case "$(codesign -dv --verbose=4 "$file" 2>&1 | sed -n 's/^Flags=//p' | head -n 1)" in
    *runtime*) ;;
    *) echo "::error::$file was not signed with the hardened runtime" >&2; exit 1 ;;
  esac
  echo "signed: $file"
done

work="${RUNNER_TEMP:-/tmp}/rdev-notarize"
rm -rf "$work"
mkdir -p "$work"

notarize() {
  local subject=$1 output status
  output="$work/notary-$(basename "$subject").json"
  xcrun notarytool submit "$subject" --keychain-profile "$profile" --keychain "$keychain" \
    --wait --output-format json > "$output"
  status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["status"])' "$output")"
  if [ "$status" != "Accepted" ]; then
    echo "::error::notarization of $subject status: $status" >&2
    python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1])), indent=2))' "$output" >&2 || true
    exit 1
  fi
  echo "notarized: $subject ($status)"
}

for path in "$@"; do
  case "$path" in
    *.zip | *.dmg | *.pkg)
      notarize "$path"
      ;;
    *)
      archive="$work/$(basename "$path").zip"
      ditto -c -k --sequesterRsrc --keepParent "$path" "$archive"
      notarize "$archive"
      ;;
  esac
  case "$path" in
    *.app | *.dmg | *.pkg)
      # Only bundles and disk images can carry a stapled ticket.
      xcrun stapler staple "$path"
      xcrun stapler validate "$path"
      ;;
  esac
done

echo "macOS signing complete: ${#mapfile_targets[@]} Mach-O file(s)"
