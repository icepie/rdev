#!/usr/bin/env bash
# Import the Developer ID certificate and the notary API key from the shared
# nicecode-tools signing secrets into a throwaway keychain, then export the
# identity/profile for the signing steps.
#
# Inputs (environment):
#   MACOS_CERT_P12_BASE64  base64 of the Developer ID Application .p12
#   MACOS_CERT_PASSWORD    password of that .p12
#   APPLE_API_KEY_ID       App Store Connect API key id
#   APPLE_API_ISSUER       App Store Connect issuer uuid
#   APPLE_API_KEY_P8       contents of the AuthKey_<id>.p8 file
#
# Outputs: APPLE_SIGNING_IDENTITY, APPLE_NOTARY_PROFILE, APPLE_NOTARY_KEYCHAIN
# (written to $GITHUB_ENV when present, otherwise only exported here).
set -euo pipefail

require() {
  local name=$1
  if [ -z "${!name:-}" ]; then
    echo "::error::$name is required to sign macOS artifacts" >&2
    exit 1
  fi
}

require MACOS_CERT_P12_BASE64
require MACOS_CERT_PASSWORD
require APPLE_API_KEY_ID
require APPLE_API_ISSUER
require APPLE_API_KEY_P8

work="${RUNNER_TEMP:-/tmp}"
cert="$work/rdev-signing.p12"
keychain="$work/rdev-signing.keychain-db"
api_key="$work/rdev-notary.p8"
profile="rdev-ci"
keychain_password="rdev-ci-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}-${RANDOM}"

rm -f "$cert" "$api_key"
printf '%s' "$MACOS_CERT_P12_BASE64" | base64 --decode > "$cert"
printf '%s' "$APPLE_API_KEY_P8" > "$api_key"
chmod 600 "$cert" "$api_key"

security delete-keychain "$keychain" >/dev/null 2>&1 || true
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$cert" -k "$keychain" -P "$MACOS_CERT_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null
security list-keychains -d user -s "$keychain" "$HOME/Library/Keychains/login.keychain-db"

identity="$(security find-identity -v -p codesigning "$keychain" | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$identity" ]; then
  echo "::error::no Developer ID Application identity found in the imported certificate" >&2
  exit 1
fi
xcrun notarytool store-credentials "$profile" --key "$api_key" --key-id "$APPLE_API_KEY_ID" \
  --issuer "$APPLE_API_ISSUER" --keychain "$keychain" >/dev/null

export APPLE_SIGNING_IDENTITY="$identity"
export APPLE_NOTARY_PROFILE="$profile"
export APPLE_NOTARY_KEYCHAIN="$keychain"
if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "APPLE_SIGNING_IDENTITY=$identity"
    echo "APPLE_NOTARY_PROFILE=$profile"
    echo "APPLE_NOTARY_KEYCHAIN=$keychain"
  } >> "$GITHUB_ENV"
fi
echo "Apple signing ready: $identity"
