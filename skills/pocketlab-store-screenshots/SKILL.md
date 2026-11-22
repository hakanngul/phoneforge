---
name: pocketlab-store-screenshots
description: Capture App Store / Google Play listing screenshots from a real device or simulator with Pocketlab - navigate to the key screens, capture them, resize to the store-required dimensions, and produce a folder plus a simple HTML gallery. Use for "app store screenshots", "play store screenshots", "store listing images", "capture marketing screens", or in Turkish "mağaza ekran görüntüleri", "App Store görselleri", "Play Store ekran görüntüsü hazırla".
---

# Pocketlab store screenshots

Produces raw, unframed captures at store-valid sizes. Device framing, captions and marketing overlays are a
separate design step; this skill gets the right pixels and an overview page.
Sizes and rules: `references/store-sizes.md`. Navigation basics: `pocketlab-device-control`.

## Pick the capture device first
Store sizes are fixed; the cleanest result comes from capturing on a device whose native resolution already
matches, so no scaling is needed.
- iOS: a simulator is ideal (`list_simulators`, `boot_simulator`, then `start_bridge` on `sim-<udid>`). Choose a
  model whose native screenshot matches a required slot (e.g. a 6.9" iPhone for the 1320x2868 slot).
  Real iPhones work too (`start_bridge` needs the WDA helper installed).
- Android: a phone or emulator with a normal 9:16-ish aspect. Play accepts 320-3840 px per side, max 2:1 ratio.
Confirm size with `get_device` / the first screenshot's pixel dimensions (`sips -g pixelWidth -g pixelHeight f.png`).

## Procedure
1. Ask (or infer from the repo) the app id, the 4-8 screens that sell the app, and the language. Plan the order:
   strongest feature first. Stop and ask if login or seeded data is needed; do not make up accounts.
2. Prepare a clean, attractive state: demo data loaded, no debug banners or keyboards, notifications dismissed,
   light/dark as requested. Optional status-bar tidy-up is manual (simulator: `xcrun simctl status_bar booted override`).
3. `launch_app` (fresh). For each planned screen: navigate (taps/swipes via the observe-act-verify loop),
   wait until the screen has settled (tree identical across two reads, spinners gone), confirm the expected title
   text with `ui_tree filter=...`, then `get_screenshot` and save it as
   `store-screenshots/<platform>/<NN>-<slug>.png`. How to save: the CLI does it directly:
   `pocketlab screenshot -d <id> -o store-screenshots/ios/01-home.png`; via MCP, write the returned PNG to disk.
4. Review each capture (look at it): no keyboard, no system alert, no truncated data, correct locale. Retake
   bad ones; do not ship a capture you have not looked at.
5. Normalize sizes if the capture does not already match a slot (below), keeping originals in `raw/`.
6. Build the gallery and report the folder, the list of files with pixel sizes, and anything that needs the
   user's decision (missing slot, scaling used).

## Resizing (only when needed)
Never stretch to a different aspect ratio. Same aspect, different scale:
```bash
sips -z <height> <width> in.png --out out.png            # macOS built-in; note order is height then width
magick in.png -resize 1320x2868 out.png                  # ImageMagick, if installed
```
Different aspect: capture again on a better-matched device, or letterbox/crop deliberately and tell the user.
Verify every output: `sips -g pixelWidth -g pixelHeight out.png`. Keep PNG (or JPEG); App Store disallows alpha
transparency in screenshots, so flatten if needed: `magick in.png -background white -alpha remove -alpha off out.png`.

## Gallery
Write `store-screenshots/index.html` (self-contained, relative image paths) with one section per platform/size:
```bash
cd store-screenshots && {
 echo '<!doctype html><meta charset=utf-8><title>Store screenshots</title>'
 echo '<style>body{font:14px system-ui;margin:24px}img{height:420px;margin:0 12px 12px 0;border:1px solid #ccc}</style>'
 for d in ios android; do [ -d $d ] || continue; echo "<h2>$d</h2>"
   for f in $d/*.png; do w=$(sips -g pixelWidth "$f" | awk '/pixelWidth/{print $2}'); h=$(sips -g pixelHeight "$f" | awk '/pixelHeight/{print $2}')
     echo "<figure style='display:inline-block'><img src='$f'><figcaption>$(basename $f) - ${w}x${h}</figcaption></figure>"; done; done
} > index.html
```
(`sips` is macOS only; elsewhere read sizes with `identify`.) Open it for the user: `open index.html`.

## Output layout
```
store-screenshots/
  raw/                  originals as captured
  ios/01-home.png ...   final, store-sized
  android/01-home.png ...
  index.html
```

## Pitfalls
- iOS screenshot pixels != tap points. Navigate with tree coordinates, capture with `get_screenshot`.
- Store review rejects screenshots showing other brands' UI, personal data, or placeholder content: use demo data.
- Each store has min/max counts and per-locale sets; list what you produced against the checklist in the
  reference, do not claim compliance beyond the dimensions you verified.
