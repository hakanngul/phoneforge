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

Behaviour of the `app` job, decided by which repository secrets exist:

| Secrets present | Result |
| --- | --- |
| none of the below | ad-hoc signed, not notarized |
| `APPLE_CERT_P12_BASE64` + `APPLE_CERT_PASSWORD` + `APPLE_TEAM_ID` | Developer ID signed (hardened runtime), not notarized |
| the three above, plus `ASC_KEY_ID` + `ASC_ISSUER_ID` + `ASC_KEY_P8_BASE64` (preferred) | signed, notarized with the App Store Connect API key, stapled |
| the three cert secrets, plus `APPLE_ID` + `APPLE_APP_PASSWORD` (fallback) | signed, notarized with Apple ID + app-specific password, stapled |

If both notarization credential sets exist, the API key is used. Notarization never runs without the signing
secrets, because a Developer ID signature is required. The release notes header says whether the macOS app is
notarized, signed only, or ad-hoc.

### Prerequisite: a Developer ID Application certificate

An "Apple Development" identity cannot be notarized. Create a "Developer ID Application" certificate
(Account Holder role required):

1. Xcode > Settings > Accounts > select the team > Manage Certificates... > `+` > Developer ID Application.
2. Check: `security find-identity -v -p codesigning | grep "Developer ID Application"`.
3. Export it: Keychain Access > My Certificates > right-click the "Developer ID Application: ..." entry > Export...
   > save as `DeveloperID.p12` with a password.

### One-time GitHub secrets setup (you run these)

Values are read from your shell or stdin, so nothing is typed into a command line or printed. The ASC_* env vars
(`ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_KEY_PATH` pointing at the `.p8`) must be set in your shell.

```sh
REPO=hakanngul/phoneforge

# Notarization: App Store Connect API key (preferred)
gh secret set ASC_KEY_ID    --repo $REPO --body "$ASC_KEY_ID"
gh secret set ASC_ISSUER_ID --repo $REPO --body "$ASC_ISSUER_ID"
base64 -i "$ASC_KEY_PATH" | gh secret set ASC_KEY_P8_BASE64 --repo $REPO

# Signing: Developer ID Application certificate
base64 -i DeveloperID.p12 | gh secret set APPLE_CERT_P12_BASE64 --repo $REPO
gh secret set APPLE_CERT_PASSWORD --repo $REPO      # prompts for the .p12 password (hidden input)
gh secret set APPLE_TEAM_ID --repo $REPO --body "<10-character team id>"

# Optional fallback instead of the API key: Apple ID + app-specific password
# gh secret set APPLE_ID --repo $REPO --body "you@example.com"
# gh secret set APPLE_APP_PASSWORD --repo $REPO       # prompts (hidden input)

gh secret list --repo $REPO
```

Then test with Actions > Release > Run workflow (builds only; the Notarize step runs when the secrets exist), or
push a tag.

With secrets present the workflow imports the certificate into a temporary keychain, signs with
`PF_CODESIGN_IDENTITY` (hardened runtime, `desktop/entitlements.plist`), runs
`xcrun notarytool submit --wait` on the dmg (`--key/--key-id/--issuer` for the API key; the key file is written
to the runner temp dir with mode 0600 and removed afterwards), and staples the dmg and the app.

### Local signing and notarization

```sh
scripts/build-desktop.sh --dmg
PF_CODESIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" scripts/notarize-local.sh --dmg
```

`scripts/notarize-local.sh` re-signs `dist/PhoneForge.app` with hardened runtime, submits it with the
`ASC_KEY_ID`, `ASC_ISSUER_ID` and `ASC_KEY_PATH` from your shell (the key file is read by `notarytool` at runtime;
no secret is echoed), staples, and verifies with `spctl -a -vv`. `--dmg` also signs, notarizes and staples
`dist/PhoneForge.dmg`. If no Developer ID Application certificate is in your keychain it stops and prints how to
create one.

`scripts/build-desktop.sh` honours `PF_CODESIGN_IDENTITY` (default `-`) and `PF_KEYCHAIN` locally too.
