#!/usr/bin/env bash
# Build dist/PhoneForge.app (ad-hoc signed) and optionally dist/PhoneForge.dmg.
#   scripts/build-desktop.sh [--dmg] [--dev]
#     --dmg  also create dist/PhoneForge.dmg
#     --dev  enable Toggle DevTools / web inspector in the app
#   Environment:
#     PF_VERSION            version to stamp (Info.plist + Go binary); default 0.1.0-dev
#     PF_CODESIGN_IDENTITY  codesign identity (e.g. "Developer ID Application: ... (TEAMID)");
#                           default "-" (ad-hoc). A real identity also enables the hardened runtime.
#     PF_KEYCHAIN           optional keychain path to search for the identity
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
DMG=0; DEV=0
for a in "$@"; do
  case "$a" in
    --dmg) DMG=1 ;;
    --dev) DEV=1 ;;
    *) echo "unknown option $a" >&2; exit 2 ;;
  esac
done

VERSION=${PF_VERSION:-$(sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/version/version.go | head -1)}
VERSION=${VERSION#v}
IDENTITY=${PF_CODESIGN_IDENTITY:--}
APP=dist/PhoneForge.app
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# --- icons: SVG -> PNG -> .icns (sips + iconutil) --------------------------
render() { sips -s format png -z "$2" "$2" "$1" --out "$3" >/dev/null; }
render desktop/icon.svg 1024 "$WORK/icon1024.png"
render desktop/tray.svg 44 cmd/phoneforge-desktop/assets/tray.png
cp "$WORK/icon1024.png" "$WORK/icon512.png"
sips -z 512 512 "$WORK/icon512.png" --out cmd/phoneforge-desktop/assets/icon.png >/dev/null
ICONSET="$WORK/PhoneForge.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s "$WORK/icon1024.png" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) "$WORK/icon1024.png" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$WORK/PhoneForge.icns"

# --- build the binary (cgo, Wails v3) --------------------------------------
LDFLAGS="-s -w -X phoneforge/internal/version.Version=$VERSION"
[ $DEV = 1 ] && LDFLAGS="$LDFLAGS -X main.devBuild=1"
mkdir -p dist
MACOSX_DEPLOYMENT_TARGET=13.0 CGO_ENABLED=1 CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" \
  go build -ldflags "$LDFLAGS" -o "$WORK/phoneforge-desktop" ./cmd/phoneforge-desktop

# --- assemble the bundle ---------------------------------------------------
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK/phoneforge-desktop" "$APP/Contents/MacOS/phoneforge-desktop"
cp "$WORK/PhoneForge.icns" "$APP/Contents/Resources/PhoneForge.icns"
# Pinned appium/WebDriverAgent source (BSD): PhoneForge builds/signs the iOS helper on the user's own
# Mac ("Build WDA" in the app), so the source ships inside the bundle; no git or network needed there.
WDA_TARBALL=$(scripts/fetch-wda.sh)
cp "$WDA_TARBALL" "$APP/Contents/Resources/$(basename "$WDA_TARBALL")"
WDA_LICENSE=$(tar -tzf "$WDA_TARBALL" | awk '/^[^\/]+\/LICENSE$/ && !f {print; f=1}')
[ -n "$WDA_LICENSE" ] || { echo "error: no top-level LICENSE found in $WDA_TARBALL" >&2; exit 1; }
tar -xzOf "$WDA_TARBALL" "$WDA_LICENSE" > "$APP/Contents/Resources/WebDriverAgent-LICENSE.txt"
sed "s/__VERSION__/$VERSION/g" desktop/Info.plist > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null

# --- sign: ad-hoc by default, Developer ID (hardened runtime + timestamp) when PF_CODESIGN_IDENTITY is set ---
PF_CODESIGN_IDENTITY=$IDENTITY scripts/sign-macos.sh app "$APP"
echo "built $APP (version $VERSION, identity $IDENTITY)"

if [ $DMG = 1 ]; then
  PF_CODESIGN_IDENTITY=$IDENTITY scripts/sign-macos.sh mkdmg "$APP" dist/PhoneForge.dmg
  echo "built dist/PhoneForge.dmg"
fi
