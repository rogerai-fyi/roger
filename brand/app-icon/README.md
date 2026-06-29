# RogerAI — iOS app icon

The app icon for the (future) RogerAI iOS app, built from **Ping**, the mascot
(`web/src/ping.svg`): the `[ ]` bracket monogram + the live-red on-air beacon eye, on a
warm-ink ground. Monochrome + one red, matching `favicon.svg` / `logo.svg` / `ping.svg`.

## Files

```
roger-icon.svg            master (edit this) — full-bleed 1024 square
roger-icon-1024.png       rendered master — App Store / marketing 1024×1024
roger-icon-180.png        preview (iPhone @3x)
AppIcon.appiconset/       drop straight into an Xcode asset catalog
  Contents.json           single-size (1024) — Xcode 14+ derives every size
  icon-1024.png
```

## iOS rules baked in

- **Full-bleed square, no rounded corners, no transparency.** iOS applies its own
  superellipse ("squircle") mask — pre-rounding double-rounds it.
- The glyph sits in a ~22% safe-area margin so the mask never clips it.
- sRGB, fully opaque.

## Regenerate

The PNGs are rendered from `roger-icon.svg` with [`resvg`](https://github.com/linebender/resvg):

```sh
cd brand/app-icon
resvg roger-icon.svg roger-icon-1024.png
resvg roger-icon.svg roger-icon-180.png --width 180 --height 180
cp roger-icon-1024.png AppIcon.appiconset/icon-1024.png
```

(`rsvg-convert roger-icon.svg -w 1024 -h 1024 -o roger-icon-1024.png` works too.)

## Use in Xcode

Drag `AppIcon.appiconset` into `Assets.xcassets`, or set the target's App Icon to it.
The single 1024 source is enough — Xcode 14+ generates the home-screen / Settings /
Spotlight sizes automatically.
