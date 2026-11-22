#!/usr/bin/env bash
# Sign a Pocketlab.app (and optionally a .dmg) inside-out.
#   scripts/sign-macos.sh app <path.app>
#   scripts/sign-macos.sh dmg <path.dmg>
#   scripts/sign-macos.sh mkdmg <path.app> <out.dmg>   # build a dmg from the app, then sign it
# Environment: PL_CODESIGN_IDENTITY ("" or "-" = ad-hoc), PL_KEYCHAIN (optional).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/_env-compat.sh
IDENTITY=${PL_CODESIGN_IDENTITY:--}
[ -n "$IDENTITY" ] || IDENTITY=-
ENT=desktop/entitlements.plist

common=(--force -s "$IDENTITY")
if [ "$IDENTITY" != "-" ]; then
  common+=(--options runtime --timestamp)
  [ -n "${PL_KEYCHAIN:-}" ] && common+=(--keychain "$PL_KEYCHAIN")
fi

sign_app() {
  local app=$1 f
  # Nested Mach-O files first (anything but the main executable), then nested bundles, then the app.
  while IFS= read -r f; do
    [ -L "$f" ] && continue
    if file -b "$f" | grep -q 'Mach-O'; then
      echo "  sign $f"
      if [ "$(basename "$f")" = plcapture ]; then
        # needs the camera entitlement (the hardened runtime blocks AVCaptureDevice otherwise)
        codesign "${common[@]}" --entitlements "$ENT" "$f"
      else
        codesign "${common[@]}" "$f"
      fi
    fi
  done < <(find "$app/Contents" -type f ! -path "$app/Contents/MacOS/pocketlab-desktop" | sort)
  while IFS= read -r f; do
    echo "  sign $f"
    codesign "${common[@]}" "$f"
  done < <(find "$app/Contents" -type d \( -name '*.framework' -o -name '*.app' -o -name '*.xpc' -o -name '*.bundle' \) | awk '{print length, $0}' | sort -rn | cut -d' ' -f2-)
  echo "  sign $app/Contents/MacOS/pocketlab-desktop"
  codesign "${common[@]}" --entitlements "$ENT" "$app/Contents/MacOS/pocketlab-desktop"
  echo "  sign $app"
  codesign "${common[@]}" --entitlements "$ENT" "$app"
  codesign --verify --strict --deep --verbose=2 "$app"
}

sign_dmg() {
  [ "$IDENTITY" = "-" ] && return 0
  local args=(--force -s "$IDENTITY" --timestamp)
  [ -n "${PL_KEYCHAIN:-}" ] && args+=(--keychain "$PL_KEYCHAIN")
  codesign "${args[@]}" "$1"
}

case "${1:-}" in
  app) sign_app "$2" ;;
  dmg) sign_dmg "$2" ;;
  mkdmg)
    stage=$(mktemp -d); trap 'rm -rf "$stage"' EXIT
    cp -R "$2" "$stage/"; ln -s /Applications "$stage/Applications"
    rm -f "$3"
    hdiutil create -volname Pocketlab -srcfolder "$stage" -ov -format UDZO "$3" >/dev/null
    sign_dmg "$3" ;;
  *) echo "usage: $0 app|dmg|mkdmg ..." >&2; exit 2 ;;
esac
