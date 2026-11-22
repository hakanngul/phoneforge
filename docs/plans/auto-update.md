# Plan: desktop auto-update

Status: proposed (2026-11-22). Owner: TBD.

## Goal
A signed `Pocketlab.app` on macOS detects, downloads, verifies and installs a newer release, then relaunches. It never installs an artifact that fails verification, and a failed install leaves the old app runnable. Windows gets notify-only first.

## Decision: custom Go updater (`internal/update`)
| Option | Verdict |
|---|---|
| Sparkle 2 via cgo | Fights Wails v3 (own NSApplication delegate, relaunch, runloop), extra framework to sign/notarize, no Windows story. Revisit only if delta updates or phased rollouts are needed. |
| go-selfupdate / minio/selfupdate | Replace a single binary; swapping only `Contents/MacOS/*` breaks the bundle signature. Fine for the CLI later, not for the app. |
| **Custom updater (stdlib)** | ~300 lines for the bundle swap, ties into the lifecycle recorder and menus, `crypto/ed25519`, `ditto`, `codesign`/`spctl`, testable with `httptest`. |

## Feed and integrity
- Each release gets `update-manifest.json` plus `update-manifest.json.sig` (detached ed25519). Fields: `version, channel, published, min_os, notes_md, notes_url, assets[{os, arch, kind, name, url, size, sha256}]`.
- Stable: `https://github.com/hakanngul/pocketlab/releases/latest/download/update-manifest.json` (no API quota; prereleases are never "latest"). Beta: newest prerelease via the API.
- Verification chain: manifest signature (embedded public keys, current + next for rotation) → size + SHA256 → `codesign --verify --deep --strict` → Team ID equals the running app's → `spctl --assess` / `stapler validate` → semver newer, same bundle id.
- New GitHub secret `UPDATE_ED25519_PRIVATE_KEY`; public key committed. Unsigned/dev builds: `PL_UPDATE_DISABLE`.

## Install flow (macOS)
1. Download the `.zip` to `~/.pocketlab/updates/<ver>/`, extract with `ditto -x -k`, verify.
2. Pre-flight: translocated app → ask the user to move it to Applications; non-writable parent → open the DMG with guidance; stage on the same volume so the swap is atomic.
3. Stop the owned server (`shutdownOwned("update to <ver>")`, lifecycle origin `updater`).
4. Swap with `renamex_np(RENAME_SWAP)`, fallback two renames; keep `~/.pocketlab/updates/backup/Pocketlab-<old>.app`.
5. Remove quarantine (after verification only), spawn a detached helper that waits for our PID and `open -n`s the new app, then quit.
6. Next start: `update.json {pending}` → lifecycle `update-ok`, or offer rollback if the version did not change or the app crashed before the "healthy" marker.

## UI and cadence
- Menu and tray: "Check for Updates…" (native dialog: Install / Remind me later / Skip this version, with notes).
- Web UI banner (`frontend/update.js`) fed by `GET /api/v1/update`; `POST /api/v1/update/{check,install,skip,remind}` served by the desktop only.
- Check 30 s after launch, then every 24 h (jittered, ETag). Never install during a test run or recording.
- Settings: auto-check, channel (stable/beta), auto-download (off by default; install always asks).

## Slices
| # | Slice | Effort | Acceptance |
|---|---|---|---|
| 1 | Manifest + verification core, `scripts/mkmanifest`, release.yml sign step | 1.5 d | Tampered manifest / wrong key / bad SHA / downgrade rejected; release attaches signed manifest |
| 2 | Check + notify (menu, tray, banner, REST) | 2 d | Reports available/current; skip & remind persist; no network when auto-check is off |
| 3 | macOS download + install + relaunch | 4 d | In-place update with lifecycle event; read-only/translocated install falls back to DMG; bad signature aborts cleanly |
| 4 | Rollback + health marker | 1.5 d | Crash before healthy marker offers rollback; manual revert passes `codesign --verify` |
| 5 | Settings + beta channel | 1 d | Toggles apply without restart; beta sees prereleases |
| 6 | Windows notify-only (then in-place, optional) | 1 d (+3 d) | |
| 7 | `pocketlab update` for CLI/server (optional) | 2 d | |

Total: ~12–14 working days for slices 1–5, ~6 more for the optional ones.

## Tests
Fake release server (`httptest`): valid update, bad signature, truncated/resumed download, SHA mismatch, 404/5xx backoff, ETag 304, downgrade. Ad-hoc signed fixture bundle for swap tests (read-only parent, backup, rollback, cross-volume). Manual QA: two local builds with a local key and `PL_UPDATE_URL` (dev builds only).

## Risks / open questions
- ed25519 key custody (secret + offline backup; two embedded keys from day one).
- Team ID pinning means ad-hoc builds cannot self-update.
- TCC grants (camera for `plcapture`) after the swap: same Team ID + identifier should keep them; confirm on a real Mac.
- Attached-server mode: an older `pocketlabd` on 8700 is not updated; show a version-mismatch note.
- The manifest URL is baked into shipped clients; decide now whether to add a signed bootstrap/redirect.

Key files: `cmd/pocketlab-desktop/{main,serverctl}.go`, `.github/workflows/release.yml`, `scripts/build-desktop.sh`, `internal/lifecycle/lifecycle.go`.
