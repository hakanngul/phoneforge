#!/usr/bin/env bash
# Fetch the pinned appium/WebDriverAgent source tarball (BSD licence) that ships inside PhoneForge.
#   scripts/fetch-wda.sh [OUT_DIR]      prints the path of the tarball on stdout
# The pin lives in internal/iosbridge/wdasrc.go (WDATag); PhoneForge extracts the tarball on the user's
# Mac on first "Build WDA" (no git, no network). The tarball is cached (default <repo>/.cache/wda,
# or $PF_CACHE_DIR/wda) and is never committed.
set -euo pipefail
cd "$(dirname "$0")/.."
TAG=$(sed -n 's/^const WDATag = "\(.*\)".*/\1/p' internal/iosbridge/wdasrc.go | head -1)
[ -n "$TAG" ] || { echo "cannot read WDATag from internal/iosbridge/wdasrc.go" >&2; exit 1; }
OUT=${1:-${PF_CACHE_DIR:-$PWD/.cache}/wda}
FILE="$OUT/WebDriverAgent-$TAG.tar.gz"
URL="https://codeload.github.com/appium/WebDriverAgent/tar.gz/refs/tags/$TAG"

valid() {
  [ -s "$1" ] || return 1
  local list; list=$(tar -tzf "$1" 2>/dev/null) || return 1
  grep -qE '^[^/]+/LICENSE$' <<<"$list" && grep -q 'WebDriverAgent.xcodeproj/project.pbxproj$' <<<"$list"
}

mkdir -p "$OUT"
if ! valid "$FILE"; then
  echo "fetching WebDriverAgent $TAG ..." >&2
  rm -f "$FILE"
  curl -fsSL --retry 3 -o "$FILE.part" "$URL"
  mv "$FILE.part" "$FILE"
  valid "$FILE" || { rm -f "$FILE"; echo "downloaded tarball for $TAG is missing LICENSE or the Xcode project" >&2; exit 1; }
fi
echo "$FILE"
