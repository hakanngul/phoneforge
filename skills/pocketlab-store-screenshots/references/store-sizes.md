# Store screenshot requirements (verify against current store docs before submitting)

These values are the commonly published requirements at the time of writing; stores change them. Treat the
live App Store Connect / Play Console upload dialogs as the source of truth.

## Apple App Store (App Store Connect)
- 1 to 10 screenshots per device size and localization. PNG or JPEG, RGB, no alpha/transparency.
- Provide at least the largest iPhone size; smaller iPhone sizes can be scaled from it by App Store Connect.
  Typical accepted portrait sizes (pixels, width x height):

| Slot | Size |
|---|---|
| iPhone 6.9" | 1320 x 2868 (also 1290 x 2796) |
| iPhone 6.5" | 1284 x 2778 or 1242 x 2688 |
| iPhone 6.3" | 1206 x 2622 (also 1179 x 2556) |
| iPhone 5.5" | 1242 x 2208 |
| iPad 13" | 2064 x 2752 (also 2048 x 2732) |
| iPad 11" | 1668 x 2420 (also 1668 x 2388, 1640 x 2360) |

Landscape swaps width and height.

## Google Play
- Phone: 2 to 8 screenshots. JPEG or 24-bit PNG (no alpha). Each side 320-3840 px; the longer side may be at
  most 2x the shorter side. For featured placement 1080+ px on the short side and 16:9 or 9:16 is recommended.
- 7" and 10" tablet screenshots: up to 8 each, same pixel limits.
- Feature graphic (separate asset): 1024 x 500 px.

## Common device native sizes (for choosing a capture device)
Capture dimensions equal the physical screen in pixels on iOS (points x scale: 3x on Pro iPhones, 2x on iPads
and older phones). Android varies; a 1080 x 2400 emulator already satisfies Play's phone rules.

## Checklist
- Right slot size, verified with `sips -g pixelWidth -g pixelHeight`.
- No alpha channel, no rounded device bezels baked in unless intended and allowed.
- Locale matches the listing; no debug UI, keyboards, alerts, personal data.
