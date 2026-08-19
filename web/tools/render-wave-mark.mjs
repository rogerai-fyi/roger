/* Render the Wave Spectrum mark to frames, for MP4/GIF export.
   ---------------------------------------------------------------------------
   This is NOT a screen recording. It re-runs the SAME standing-wave math the
   live mark uses (src/js/wave-mark-spectrum.js), so every frame is exact
   vector output rather than a compressed capture of a browser. Keep the two in
   step: the constants below are copied from that file and the shapes are the
   same functions.

   Usage:  node tools/render-wave-mark.mjs [--theme dark|light] [--out DIR]
           [--w 1200] [--fps 25] [--seconds 16.5]
*/
import { mkdirSync, writeFileSync } from "node:fs";

const arg = (k, d) => {
  const i = process.argv.indexOf("--" + k);
  return i > 0 && process.argv[i + 1] ? process.argv[i + 1] : d;
};
const THEME   = arg("theme", "dark");
const OUT     = arg("out", "/tmp/wave-frames");
const WIDTH   = parseInt(arg("w", "1200"), 10);
const FPS     = parseInt(arg("fps", "25"), 10);
const SECONDS = parseFloat(arg("seconds", "16.5"));
/* --labels no drops the WAVE <TIER> nameplate and keeps the ROGERAI.FM
   callsign: the mark as a mark, for places where the tier names are noise
   rather than the point. The beacon still takes the firing tier's colour, so
   the spectrum still reads. */
const LABELS  = arg("labels", "yes") !== "no";

/* ---- constants, mirrored from js/wave-mark-spectrum.js -------------------- */
const X0 = 0, X1 = 348, CX = 174, CY = 78, N = 84, TWO_PI = Math.PI * 2;
const SPAN = 16.5, GAP = 2.4, INF = 7, TOTAL = 8 + GAP;
const START = (8 + GAP / 2) / TOTAL;
const TIERS = [
  { id: "pico",  name: "PICO",  k: 7, amp: 13 },
  { id: "nano",  name: "NANO",  k: 6, amp: 19 },
  { id: "micro", name: "MICRO", k: 5, amp: 26 },
  { id: "giga",  name: "GIGA",  k: 4, amp: 34 },
  { id: "tera",  name: "TERA",  k: 3, amp: 42 },
  { id: "peta",  name: "PETA",  k: 2, amp: 50 },
  { id: "exa",   name: "EXA",   k: 1, amp: 58 },
];
const PALETTE = {
  /* ground/ink/ident are the real tokens from styles/tokens.css, not guesses:
     --paper, --ink-900 and --ink-400 for each theme. The ident used the
     HAIRLINE colour at first and nearly vanished on the dark ground. */
  light: { pico:"#b23a2a", nano:"#c96a1c", micro:"#b0891a", giga:"#2f8a52",
           tera:"#1f8f8f", peta:"#2f63bf", exa:"#5b3fbf",
           ground:"#FBFBFA", ink:"#15140F", ident:"#6B685F", live:"#e0231c" },
  dark:  { pico:"#e6604f", nano:"#e88b3c", micro:"#d4aa2e", giga:"#48b873",
           tera:"#39b7b7", peta:"#5b8ee6", exa:"#8a6df0",
           ground:"#0E0D0B", ink:"#F3F1EA", ident:"#9A968B", live:"#e0231c" },
}[THEME];

function pathFor(t, charge, u) {
  const L = X1 - X0;
  const drift = 0.88 + 0.12 * Math.sin(TWO_PI * u * (1 + (t.k % 2)) + t.k);
  const A = t.amp * drift * (1 + 0.42 * charge);
  let d = "";
  for (let i = 0; i <= N; i++) {
    const x = X0 + (L * i) / N, uu = (x - CX) / L;
    d += (i === 0 ? "M" : "L") + x.toFixed(2) + " " + (CY + A * Math.sin(t.k * TWO_PI * uu)).toFixed(2);
  }
  return d;
}

function frame(u) {
  const phase = u * TOTAL;
  let dInf = phase - INF;
  if (dInf >  TOTAL / 2) dInf -= TOTAL;
  if (dInf < -TOTAL / 2) dInf += TOTAL;
  const inf = dInf > -1.6 && dInf < 1.6 ? Math.exp(-(dInf * dInf) * 2.1) : 0;

  let best = 0, lead = null, paths = "";
  for (let i = 0; i < TIERS.length; i++) {
    const t = TIERS[i];
    let dd = phase - i;
    if (dd >  TOTAL / 2) dd -= TOTAL;
    if (dd < -TOTAL / 2) dd += TOTAL;
    const own = dd > -1.6 && dd < 1.6 ? Math.exp(-(dd * dd) * 2.1) : 0;
    const charge = Math.max(own, inf);
    const stroke = inf > own ? PALETTE.ink : PALETTE[t.id];
    paths += `<path d="${pathFor(t, charge, u)}" fill="none" stroke="${stroke}" ` +
             `stroke-width="${(1.7 + 4.6 * charge).toFixed(2)}" stroke-linecap="round" ` +
             `opacity="${(0.2 + 0.8 * charge).toFixed(3)}"/>`;
    if (own > best) { best = own; lead = t; }
  }
  if (inf > best) { best = inf; lead = { id: null, name: "INFINITE" }; }
  const leadCol = lead && lead.id ? PALETTE[lead.id] : PALETTE.ink;

  // beacon ring + node
  const ringR = 14 + 26 * Math.max(0, best - 0.35) / 0.65;
  const ringO = (Math.max(0, best - 0.35) / 0.65 * 0.55).toFixed(3);
  const nodeR = 14 * (1 + 0.16 * best);

  // callsign with its on-air lamp, and the firing nameplate
  const showTag = LABELS && best > 0.55;
  const tagOp = showTag ? ((best - 0.55) / 0.45).toFixed(2) : 0;
  const label = showTag && lead ? "WAVE " + lead.name : "";
  const plateW = label.length * 10 + 18;
  const plate = showTag
    ? `<rect x="${(CX - plateW / 2).toFixed(1)}" y="${CY + 44 - 14}" width="${plateW}" height="20" rx="3"
         fill="${PALETTE.ground}" stroke="${leadCol}" stroke-width="1" opacity="${tagOp}"/>
       <text x="${CX}" y="${CY + 44}" text-anchor="middle" fill="${leadCol}" opacity="${tagOp}"
         font-family="DejaVu Sans Mono, monospace" font-size="15" letter-spacing="3.3">${label}</text>`
    : "";

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-6 -16 360 188" width="${WIDTH}">
  <rect x="-6" y="-16" width="360" height="188" fill="${PALETTE.ground}"/>
  <g>${paths}</g>
  <circle cx="${CX}" cy="${CY}" r="${ringR.toFixed(1)}" fill="none" stroke="${leadCol}" stroke-width="2" opacity="${ringO}"/>
  <circle cx="${CX}" cy="${CY}" r="${nodeR.toFixed(2)}" fill="${leadCol}"/>
  <circle cx="${(CX - 47).toFixed(1)}" cy="30" r="${(2.6 + 1.1 * best).toFixed(2)}" fill="${leadCol}" opacity="${(0.3 + 0.7 * best).toFixed(3)}"/>
  <text x="${CX}" y="34" text-anchor="middle" fill="${PALETTE.ident}"
    font-family="DejaVu Sans Mono, monospace" font-size="11" letter-spacing="3.3">ROGERAI.FM</text>
  ${plate}
</svg>`;
}

mkdirSync(OUT, { recursive: true });
const total = Math.round(FPS * SECONDS);
for (let f = 0; f < total; f++) {
  const u = (START + (f / total)) % 1;          // exactly one loop, same entry point as the page
  writeFileSync(`${OUT}/f${String(f).padStart(4, "0")}.svg`, frame(u));
}
console.log(`${total} frames -> ${OUT}  (${THEME}, ${WIDTH}px, ${FPS}fps, ${SECONDS}s)`);
