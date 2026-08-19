# The Wave Spectrum mark — shareable exports

Four files, one loop: seven Wave tiers as standing waves that all cross at the
beacon, sweeping Pico → Exa and closing on the Wave Infinite beat.

Two cuts of the same loop. The **labelled** one names each tier as it fires
(WAVE PICO … WAVE EXA); the **clean** one drops the nameplate and keeps only the
ROGERAI.FM callsign, for places where the tier names are noise rather than the
point. The beacon still takes the firing tier's colour in both, so the spectrum
still reads.

| file | cut | use |
|---|---|---|
| `wave-mark-dark.mp4` | labelled | social posts, iMessage, Slack — dark backgrounds |
| `wave-mark-light.mp4` | labelled | the same on light backgrounds |
| `wave-mark-dark.gif` | labelled | anywhere that will not play video |
| `wave-mark-light.gif` | labelled | " |
| `wave-mark-dark-clean.mp4` | clean | the mark alone, dark |
| `wave-mark-light-clean.mp4` | clean | the mark alone, light |
| `wave-mark-dark-clean.gif` | clean | " |
| `wave-mark-light-clean.gif` | clean | " |

MP4: 1200×628 (the 1.91:1 social-card ratio), H.264 high@4.0, **yuv420p**, 25 fps,
`+faststart`. That pixel format and profile are what make it play inline in
iMessage, X, LinkedIn and Slack rather than showing a download stub.
GIF: 480×250, 12.5 fps, 64-colour palette, infinite loop, ~1.3 MB — small enough
to send in a text.

Both are **8.24 s: one complete cycle at double the site's pace**. The loop is
seamless by construction, not by crossfade; measured, the last frame differs
from the first by ~0.3 k pixels against ~17 k for an ordinary frame step.

## Regenerating

    node tools/render-wave-mark.mjs --theme dark --out /tmp/wf --w 1200 --fps 25 --seconds 8.25
    # add --labels no for the clean cut (callsign kept, tier nameplate dropped)
    cd /tmp/wf && ls *.svg | xargs -P 8 -I{} sh -c 'rsvg-convert -w 1200 "{}" -o "${1%.svg}.png"' _ {}
    ffmpeg -framerate 25 -i f%04d.png -vf scale=1200:628:flags=lanczos \
      -c:v libx264 -profile:v high -level 4.0 -pix_fmt yuv420p -crf 18 -preset slow \
      -movflags +faststart wave-mark-dark.mp4

These are **rendered from the animation's own equations**, not screen-recorded:
`tools/render-wave-mark.mjs` re-runs the standing-wave math from
`src/js/wave-mark-spectrum.js`, so every frame is exact vector output. If that
file's constants change, re-run the tool — the header of the script lists the
values that must stay in step.
