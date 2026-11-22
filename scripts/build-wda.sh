#!/usr/bin/env bash
# Developer alternative: Pocketlab itself (app UI "Build WDA", or `pocketlab ios build-wda`) needs neither this script nor the repo.
# Build and sign Pocketlab's own copy of appium/WebDriverAgent (BSD licence).
# Usage: scripts/build-wda.sh [--install <udid>] [--sim] [--help]
#   --sim   build the unsigned iOS Simulator runner instead (output: $PL_WDA_HOME/build-sim)
# Env:   PL_TEAM_ID, PL_BUNDLE_PREFIX (default com.pocketlab), PL_WDA_HOME (default ~/.pocketlab/wda)
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/_env-compat.sh"

WDA_TAG="v16.15.3"   # pinned: latest tag from `git ls-remote --tags` at authoring time
WDA_REPO="https://github.com/appium/WebDriverAgent"
HOME_DIR="${PL_WDA_HOME:-$HOME/.pocketlab/wda}"
SRC="$HOME_DIR/src"
BUILD="$HOME_DIR/build"
BUILD_SIM="$HOME_DIR/build-sim"
SIM=0
PREFIX="${PL_BUNDLE_PREFIX:-com.pocketlab}"
RUNNER_ID="$PREFIX.WebDriverAgentRunner"
LIB_ID="$PREFIX.WebDriverAgentLib"

INSTALL_UDID=""
while [ $# -gt 0 ]; do
  case "$1" in
    --install) INSTALL_UDID="${2:-}"; [ -n "$INSTALL_UDID" ] || { echo "error: --install needs a udid" >&2; exit 2; }; shift 2 ;;
    --sim) SIM=1; shift ;;
    -h|--help) sed -n '2,6p' "$0"; exit 0 ;;
    *) echo "error: unknown argument $1" >&2; exit 2 ;;
  esac
done

die() { echo "error: $*" >&2; exit 1; }

command -v xcodebuild >/dev/null || die "xcodebuild not found. Install Xcode and run: sudo xcode-select -s /Applications/Xcode.app"
command -v git >/dev/null || die "git not found"

# 1. Source
mkdir -p "$HOME_DIR"
if [ ! -d "$SRC/.git" ]; then
  echo "==> Cloning WebDriverAgent $WDA_TAG"
  git -c advice.detachedHead=false clone --depth 1 --branch "$WDA_TAG" "$WDA_REPO" "$SRC"
else
  cur="$(git -C "$SRC" describe --tags --exact-match 2>/dev/null || true)"
  if [ "$cur" != "$WDA_TAG" ]; then
    echo "==> Updating WebDriverAgent checkout to $WDA_TAG"
    git -C "$SRC" fetch --depth 1 origin "refs/tags/$WDA_TAG:refs/tags/$WDA_TAG"
    git -C "$SRC" checkout -q "$WDA_TAG"
  fi
fi

# 1b. Simulator build: no signing, no team id needed.
if [ "$SIM" = 1 ]; then
  LOG="$HOME_DIR/build-sim.log"
  echo "==> Building WebDriverAgentRunner for the iOS Simulator (log: $LOG)"
  set +e
  ( cd "$SRC" && xcodebuild build-for-testing \
      -project WebDriverAgent.xcodeproj \
      -scheme WebDriverAgentRunner \
      -sdk iphonesimulator \
      -destination 'generic/platform=iOS Simulator' \
      -derivedDataPath "$BUILD_SIM" \
      CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY="" \
      PRODUCT_BUNDLE_IDENTIFIER="$RUNNER_ID" \
      WDA_PRODUCT_BUNDLE_IDENTIFIER="$LIB_ID" ) >"$LOG" 2>&1
  rc=$?
  set -e
  if [ $rc -ne 0 ]; then
    grep -E "error:" "$LOG" | sort -u | tail -15 >&2 || true
    die "simulator build failed (full log: $LOG)"
  fi
  XCTESTRUN="$(find "$BUILD_SIM/Build/Products" -maxdepth 1 -name '*iphonesimulator*.xctestrun' | head -1)"
  [ -n "$XCTESTRUN" ] || die "build reported success but no simulator .xctestrun under $BUILD_SIM/Build/Products"
  echo "==> Simulator build succeeded"
  echo "XCTESTRUN=$XCTESTRUN"
  exit 0
fi

# 2. Team id
team_from_cert() {
  security find-certificate -c "Apple Development" -p 2>/dev/null |
    openssl x509 -noout -subject -nameopt multiline 2>/dev/null |
    awk -F= '/organizationalUnitName/{gsub(/^ +| +$/,"",$2); print $2; exit}'
}
team_from_profiles() {
  local d="$HOME/Library/Developer/Xcode/UserData/Provisioning Profiles" f
  [ -d "$d" ] || return 0
  for f in "$d"/*.mobileprovision; do
    [ -e "$f" ] || continue
    security cms -D -i "$f" 2>/dev/null | plutil -extract TeamIdentifier.0 raw -o - - 2>/dev/null && return 0
  done
}
team_from_xcode() {
  defaults read com.apple.dt.Xcode IDEProvisioningTeams 2>/dev/null |
    awk -F'"' '/teamID/{print $2; exit} /teamID *=/{gsub(/[ ;]/,"",$0); sub(/teamID=/,"",$0); print; exit}'
}

TEAM="${PL_TEAM_ID:-}"
SRCNOTE="PL_TEAM_ID"
[ -n "$TEAM" ] || { TEAM="$(team_from_cert || true)"; SRCNOTE="signing certificate"; }
[ -n "$TEAM" ] || { TEAM="$(team_from_profiles || true)"; SRCNOTE="provisioning profile"; }
[ -n "$TEAM" ] || { TEAM="$(team_from_xcode || true)"; SRCNOTE="Xcode preferences"; }
[ -n "$TEAM" ] || die "no development team found. Open Xcode > Settings > Accounts, sign in with your Apple ID, create an 'Apple Development' certificate, or set PL_TEAM_ID=<10-char team id>."
echo "==> Using development team $TEAM (from $SRCNOTE)"
echo "==> Runner bundle id: $RUNNER_ID"

# 3. Build
LOG="$HOME_DIR/build.log"
echo "==> Building WebDriverAgentRunner (log: $LOG)"
set +e
( cd "$SRC" && xcodebuild build-for-testing \
    -project WebDriverAgent.xcodeproj \
    -scheme WebDriverAgentRunner \
    -destination 'generic/platform=iOS' \
    -derivedDataPath "$BUILD" \
    -allowProvisioningUpdates \
    DEVELOPMENT_TEAM="$TEAM" \
    CODE_SIGN_STYLE=Automatic \
    PRODUCT_BUNDLE_IDENTIFIER="$RUNNER_ID" \
    WDA_PRODUCT_BUNDLE_IDENTIFIER="$LIB_ID" ) >"$LOG" 2>&1
rc=$?
set -e
if [ $rc -ne 0 ]; then
  echo "---- last errors ----" >&2
  grep -E "error:|Provisioning|No profile|requires a development team|not registered|Developer Mode" "$LOG" | sort -u | tail -15 >&2 || true
  cat >&2 <<MSG
---------------------
Build failed (full log: $LOG). Common causes:
  * Provisioning failure: free accounts need the phone registered with your team.
    Open Xcode > Settings > Accounts (sign in), then connect the iPhone once with
    a cable, unlock it, tap "Trust", and let Xcode finish "Preparing device".
    Free accounts also limit the number of app ids per week; try another PL_BUNDLE_PREFIX.
  * Developer Mode off on the phone: Settings > Privacy & Security > Developer Mode > On (reboots).
  * Untrusted developer when launching later: Settings > General > VPN & Device Management > trust your Apple Development profile.
  * No team / no certificate: see Xcode > Settings > Accounts, or set PL_TEAM_ID.
MSG
  exit 1
fi

APP="$(find "$BUILD/Build/Products" -maxdepth 2 -type d -name 'WebDriverAgentRunner-Runner.app' | head -1)"
XCTESTRUN="$(find "$BUILD/Build/Products" -maxdepth 1 -name '*.xctestrun' | head -1)"
[ -n "$APP" ] || die "build reported success but WebDriverAgentRunner-Runner.app not found under $BUILD/Build/Products"
echo "==> Build succeeded"
echo "RUNNER_APP=$APP"
echo "XCTESTRUN=${XCTESTRUN:-<none>}"

# 4. Optional install
if [ -n "$INSTALL_UDID" ]; then
  echo "==> Installing on device $INSTALL_UDID"
  ( cd "$ROOT" && go run ./cmd/pl-ios-install "$INSTALL_UDID" "$APP" ) || die "install failed. Ensure the phone is unlocked, trusted, and Developer Mode is on; then trust the developer under Settings > General > VPN & Device Management."
  echo "Installed. If launching fails with an untrusted developer error, trust it in Settings > General > VPN & Device Management."
fi
