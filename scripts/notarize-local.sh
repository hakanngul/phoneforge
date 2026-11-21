#!/usr/bin/env bash
# Sign dist/PhoneForge.app with a Developer ID Application identity, notarize it with an
# App Store Connect API key, staple the ticket and verify with spctl.
#
# Usage:
#   PF_CODESIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" scripts/notarize-local.sh [--dmg]
#
# Environment (read from your shell; values are never printed):
#   PF_CODESIGN_IDENTITY  Developer ID Application identity (name or SHA-1)
#   ASC_KEY_ID            App Store Connect API key id
#   ASC_ISSUER_ID         issuer id
#   ASC_KEY_PATH          path to the AuthKey_XXXX.p8 file (read at runtime by notarytool)
#
# --dmg  also notarize and staple dist/PhoneForge.dmg (build it first: scripts/build-desktop.sh --dmg).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

DMG=0
for a in "$@"; do
  case "$a" in
    --dmg) DMG=1 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a" >&2; exit 2 ;;
  esac
done

APP=dist/PhoneForge.app
[ -d "$APP" ] || { echo "error: $APP not found. Build it first: scripts/build-desktop.sh" >&2; exit 1; }

# --- Developer ID certificate check ---------------------------------------
if ! security find-identity -v -p codesigning | grep -q "Developer ID Application"; then
  cat >&2 <<'MSG'
error: no "Developer ID Application" certificate found in your keychains.

This Mac only has development identities (for example "Apple Development"), which cannot be
notarized. Create a Developer ID Application certificate (requires the Account Holder role):

  1. Xcode > Settings > Accounts > select your Apple ID/team > Manage Certificates...
  2. Click "+" > "Developer ID Application".
  3. Confirm it exists:  security find-identity -v -p codesigning | grep "Developer ID Application"
  4. For CI, export it: Keychain Access > My Certificates > right-click the certificate >
     Export... > .p12 (set a password), then see docs/release.md for the `gh secret set` commands.

Then re-run with:
  PF_CODESIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" scripts/notarize-local.sh
MSG
  exit 1
fi

if [ -z "${PF_CODESIGN_IDENTITY:-}" ]; then
  echo "error: set PF_CODESIGN_IDENTITY to one of these identities:" >&2
  security find-identity -v -p codesigning | grep "Developer ID Application" >&2
  exit 1
fi
if ! security find-identity -v -p codesigning | grep "Developer ID Application" | grep -qF "$PF_CODESIGN_IDENTITY"; then
  echo "error: PF_CODESIGN_IDENTITY does not match a Developer ID Application identity. Available:" >&2
  security find-identity -v -p codesigning | grep "Developer ID Application" >&2
  exit 1
fi

# --- App Store Connect API key check (never prints values) -----------------
missing=()
for v in ASC_KEY_ID ASC_ISSUER_ID ASC_KEY_PATH; do
  [ -n "${!v:-}" ] || missing+=("$v")
done
if [ ${#missing[@]} -gt 0 ]; then
  echo "error: missing environment variable(s): ${missing[*]}" >&2
  echo "       (App Store Connect > Users and Access > Integrations > Team Keys)" >&2
  exit 1
fi
[ -r "$ASC_KEY_PATH" ] || { echo "error: ASC_KEY_PATH is not a readable file" >&2; exit 1; }

# --- sign ------------------------------------------------------------------
echo "==> signing $APP"
export PF_CODESIGN_IDENTITY
scripts/sign-macos.sh app "$APP"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
notarize() { # <file> ; --key is read by notarytool at runtime
  xcrun notarytool submit "$1" --key "$ASC_KEY_PATH" --key-id "$ASC_KEY_ID" --issuer "$ASC_ISSUER_ID" --wait
}

# --- notarize + staple the app --------------------------------------------
echo "==> notarizing app (this can take a few minutes)"
ditto -c -k --keepParent "$APP" "$WORK/PhoneForge.zip"
notarize "$WORK/PhoneForge.zip"
xcrun stapler staple "$APP"
xcrun stapler validate "$APP"

# --- optional dmg ---------------------------------------------------------
if [ "$DMG" = 1 ]; then
  echo "==> rebuilding dist/PhoneForge.dmg from the signed app, signing and notarizing it"
  # Always rebuild: an existing dmg may embed an older (ad-hoc signed) copy of the app.
  scripts/sign-macos.sh mkdmg "$APP" dist/PhoneForge.dmg
  notarize dist/PhoneForge.dmg
  xcrun stapler staple dist/PhoneForge.dmg
  xcrun stapler validate dist/PhoneForge.dmg
fi

# --- verify ---------------------------------------------------------------
echo "==> Gatekeeper assessment"
spctl -a -vv "$APP"
echo "done: $APP is signed, notarized and stapled"
