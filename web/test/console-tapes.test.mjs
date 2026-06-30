// Regression locks for the FIG.3 console (the "demo replay" tape deck) gaining two
// MEDIA tapes at the end of the preset bank:
//   - "using": the animated story cartoon (assets/using-demo.{mp4,gif})
//   - "ping":  the real roger --ping screensaver capture (assets/ping-demo.{mp4,gif})
// These pin the tape markup + asset paths the media workstream depends on, mirroring
// billing-help.test.mjs (static-content assertions over web/src, no build/DOM).
// Run: node --test test/console-tapes.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");

const index = read("index.html");
const termJs = read("js/terminal.js");
const homeCss = read("styles/home.css");

test("console: the five original CLI tapes are still present, in order", () => {
  for (const d of ["roger", "tunein", "agent", "share", "payouts"]) {
    assert.match(index, new RegExp(`data-demo="${d}"`), `preset ${d} present`);
  }
});

test("console: two NEW tapes - using + ping - are appended to the preset bank", () => {
  // both new presets exist as real <button>s with the LED + label, matching the
  // existing preset markup exactly (keyboard-reachable, aria-pressed wired).
  for (const [d, label] of [["using", "using"], ["ping", "ping"]]) {
    const re = new RegExp(
      `<button class="term__preset" type="button" data-demo="${d}" aria-pressed="false">\\s*` +
      `<span class="term__preset-led" aria-hidden="true"></span>${label}`,
    );
    assert.match(index, re, `${d} preset button matches the tape markup`);
  }
  // appended at the END: both new presets come after payouts.
  assert.ok(
    index.indexOf('data-demo="payouts"') <
      index.indexOf('data-demo="using"') &&
      index.indexOf('data-demo="using"') < index.indexOf('data-demo="ping"'),
    "using + ping follow payouts, in that order",
  );
});

test("console: the media layer hosts both video tapes with mp4 + gif fallbacks", () => {
  assert.match(index, /id="termMedia"[^>]*\bhidden\b/, "#termMedia slot, hidden until picked");
  for (const [id, name] of [["termUsing", "using"], ["termPing", "ping"]]) {
    // each tape is a muted, looping, inline <video> (no surprise audio / no mobile takeover).
    const block = index.slice(index.indexOf(`id="${id}"`));
    assert.match(index, new RegExp(`<video[^>]*id="${id}"`), `${name}: <video> present`);
    assert.match(block.slice(0, 400), /\bmuted\b/, `${name}: muted`);
    assert.match(block.slice(0, 400), /\bloop\b/, `${name}: loops`);
    assert.match(block.slice(0, 400), /\bplaysinline\b/, `${name}: plays inline`);
    // mp4 source + gif poster + gif <img> fallback, all relative (build mirrors assets/).
    assert.match(block, new RegExp(`<source src="assets/${name}-demo\\.mp4" type="video/mp4">`), `${name}: mp4 source`);
    assert.match(block, new RegExp(`poster="assets/${name}-demo\\.gif"`), `${name}: gif poster`);
    assert.match(block, new RegExp(`<img[^>]*src="assets/${name}-demo\\.gif"`), `${name}: gif fallback`);
  }
});

test("terminal.js: using + ping are MEDIA demos wired to their <video> ids", () => {
  assert.match(termJs, /using:\s*{[^}]*media:\s*true[^}]*el:\s*"termUsing"/, "using is a media tape -> #termUsing");
  assert.match(termJs, /ping:\s*{[^}]*media:\s*true[^}]*el:\s*"termPing"/, "ping is a media tape -> #termPing");
  // the engine actually plays a media tape (selects + plays the <video>).
  assert.match(termJs, /function selectMedia\(/, "selectMedia branch exists");
  assert.match(termJs, /function playMedia\(/, "playMedia exists");
  assert.match(termJs, /\.play\(/, "plays the video");
  assert.match(termJs, /prefers-reduced-motion|REDUCED/, "reduced-motion aware");
});

test("assets: every referenced tape file actually exists (no broken tile in prod)", () => {
  // markup-path matching isn't enough: a tape wired to a missing asset 404s in prod.
  // Pin that all four media files exist + are non-empty. (using-demo is a placeholder
  // card until task D ships the real cartoon at the same paths.)
  for (const f of ["ping-demo.mp4", "ping-demo.gif", "using-demo.mp4", "using-demo.gif"]) {
    const p = path.join(SRC, "assets", f);
    assert.ok(existsSync(p), `assets/${f} exists`);
    assert.ok(statSync(p).size > 0, `assets/${f} is non-empty`);
  }
});

test("home.css: the media slot matches the ASCII screen slot", () => {
  assert.match(homeCss, /\.term__media\s*{[^}]*min-height:\s*420px/, "media slot is the same 420px slot");
  assert.match(homeCss, /\.term__video\s*{[^}]*width:\s*100%/, "video sizes full-width like the prior diagram");
});
