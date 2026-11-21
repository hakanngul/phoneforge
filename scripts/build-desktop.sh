#!/usr/bin/env bash
# Build dist/PhoneForge.app (ad-hoc signed) and optionally dist/PhoneForge.dmg.
#   scripts/build-desktop.sh [--dmg] [--dev]
#     --dmg  also create dist/PhoneForge.dmg
#     --dev  enable Toggle DevTools / web inspector in the app
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

VERSION=$(sed -n 's/.*appVersion *= *"\(.*\)".*/\1/p' cmd/phoneforge-desktop/main.go | head -1)
VERSION=${VERSION:-0.1.0}
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
LDFLAGS="-s -w"
[ $DEV = 1 ] && LDFLAGS="$LDFLAGS -X main.devBuild=1"
mkdir -p dist
MACOSX_DEPLOYMENT_TARGET=13.0 CGO_ENABLED=1 CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" \
  go build -ldflags "$LDFLAGS" -o "$WORK/phoneforge-desktop" ./cmd/phoneforge-desktop

# --- assemble the bundle ---------------------------------------------------
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK/phoneforge-desktop" "$APP/Contents/MacOS/phoneforge-desktop"
cp "$WORK/PhoneForge.icns" "$APP/Contents/Resources/PhoneForge.icns"
sed "s/__VERSION__/$VERSION/g" desktop/Info.plist > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null

# --- ad-hoc sign (camera entitlement so the native capture path can prompt) -
codesign --force --deep -s - --entitlements desktop/entitlements.plist "$APP"
codesign --verify --deep --strict "$APP"
echo "built $APP"

if [ $DMG = 1 ]; then
  rm -f dist/PhoneForge.dmg
  STAGE="$WORK/dmg"; mkdir -p "$STAGE"
  cp -R "$APP" "$STAGE/"
  ln -s /Applications "$STAGE/Applications"
  hdiutil create -volname PhoneForge -srcfolder "$STAGE" -ov -format UDZO dist/PhoneForge.dmg >/dev/null
  echo "built dist/PhoneForge.dmg"
fi
