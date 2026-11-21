# Releasing PhoneForge

Releases are built by `.github/workflows/release.yml`. Status: workflows added, not yet run on GitHub.

## Cut a release

```sh
git tag v0.1.0
git push origin v0.1.0
```

The tag name (without `v`) becomes the version. It is stamped into `Info.plist`
(`CFBundleShortVersionString`, `CFBundleVersion`) and into every binary through
`-X phoneforge/internal/version.Version=...` (`phoneforge version`, `/api/v1/health`, the desktop About box).
Tags containing `-` (for example `v0.2.0-rc1`) are published as pre-releases.

The workflow runs `go vet` and `go test ./...`, then builds:

| Artifact | Built on |
| --- | --- |
| `PhoneForge-<version>-macos-arm64.dmg` / `.zip` | `macos-latest` |
| `PhoneForge-<version>-macos-amd64.dmg` / `.zip` | `macos-15-intel` (best effort, failure does not block the release) |
| `phoneforge_<version>_{darwin,linux}_{arm64,amd64}.tar.gz` (`phoneforged` + `phoneforge`, CGO off) | `ubuntu-latest` |
| `SHA256SUMS.txt` | release job |

and publishes them with auto-generated release notes.

## Test run without releasing

Actions tab > Release > Run workflow (`workflow_dispatch`). It builds everything with version
`0.0.0-manual.<sha>` and uploads a `release-bundle` workflow artifact; no tag, no GitHub Release.

## Local build with a version

```sh
PF_VERSION=0.1.0 scripts/build-desktop.sh --dmg
```

## Signing and notarization (optional)

Without secrets the app is ad-hoc signed and not notarized. Users must right-click > Open on first launch, or run
`xattr -dr com.apple.quarantine /Applications/PhoneForge.app`.

To get a Developer ID signed, notarized and stapled build, add these repository secrets
(Settings > Secrets and variables > Actions). All five must be present, otherwise the workflow falls back to ad-hoc.

| Secret | Value |
| --- | --- |
| `APPLE_CERT_P12_BASE64` | `base64 -i DeveloperID.p12` of a "Developer ID Application" certificate with its private key |
| `APPLE_CERT_PASSWORD` | password used when exporting the `.p12` |
| `APPLE_TEAM_ID` | 10-character Apple team id |
| `APPLE_ID` | Apple ID email used for notarization |
| `APPLE_APP_PASSWORD` | app-specific password for that Apple ID (appleid.apple.com) |

With secrets present the workflow imports the certificate into a temporary keychain, signs with
`PF_CODESIGN_IDENTITY` (hardened runtime, `desktop/entitlements.plist`), runs
`xcrun notarytool submit --wait` on the dmg, and staples the dmg and the app. The release notes header still
contains the ad-hoc instructions; edit it once notarized builds are routine.

`scripts/build-desktop.sh` honours `PF_CODESIGN_IDENTITY` (default `-`) and `PF_KEYCHAIN` locally too.
