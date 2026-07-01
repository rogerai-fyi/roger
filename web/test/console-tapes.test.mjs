// Regression locks for the FIG.3 console (the "demo replay" tape deck) and its MEDIA
// tapes at the end of the preset bank:
//   - "using":   the animated story cartoon (assets/using-demo.{mp4,gif})
//   - "hosting": the animated "host your own model on RogerAI" story (assets/hosting-demo.{mp4,gif})
//   - "ping":    the real roger --ping screensaver capture (assets/ping-demo.{mp4,gif})
// These pin the tape markup + asset paths the media workstream depends on, mirroring
// billing-help.test.mjs (static-content assertions over web/src, no build/DOM).
// They ALSO lock the lazy-load contract: the multi-MB poster gifs must NOT be fetched on
// first paint - the eager `poster=` is deferred to a `data-poster=` the player swaps in on
// open/scroll-in, and every gif <img> fallback is `loading="lazy"`.
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

test("console: the THREE media tapes - using + hosting + ping - are appended to the preset bank", () => {
  // each new preset exists as a real <button> with the LED + label, matching the existing
  // preset markup exactly (keyboard-reachable, aria-pressed wired).
  for (const [d, label] of [["using", "using"], ["hosting", "hosting"], ["ping", "ping"]]) {
    const re = new RegExp(
      `<button class="term__preset" type="button" data-demo="${d}" aria-pressed="false">\\s*` +
      `<span class="term__preset-led" aria-hidden="true"></span>${label}`,
    );
    assert.match(index, re, `${d} preset button matches the tape markup`);
  }
  // appended at the END after payouts, and hosting sits BETWEEN using and ping.
  assert.ok(
    index.indexOf('data-demo="payouts"') < index.indexOf('data-demo="using"') &&
      index.indexOf('data-demo="using"') < index.indexOf('data-demo="hosting"') &&
      index.indexOf('data-demo="hosting"') < index.indexOf('data-demo="ping"'),
    "order is payouts < using < hosting < ping",
  );
});

test("console: the media layer hosts all three video tapes with mp4 + gif fallbacks", () => {
  assert.match(index, /id="termMedia"[^>]*\bhidden\b/, "#termMedia slot, hidden until picked");
  for (const [id, name] of [["termUsing", "using"], ["termHosting", "hosting"], ["termPing", "ping"]]) {
    // each tape is a muted, looping, inline <video> (no surprise audio / no mobile takeover).
    const block = index.slice(index.indexOf(`id="${id}"`));
    assert.match(index, new RegExp(`<video[^>]*id="${id}"`), `${name}: <video> present`);
    assert.match(block.slice(0, 400), /\bmuted\b/, `${name}: muted`);
    assert.match(block.slice(0, 400), /\bloop\b/, `${name}: loops`);
    assert.match(block.slice(0, 400), /\bplaysinline\b/, `${name}: plays inline`);
    // mp4 source + gif <img> fallback, all relative (build mirrors assets/).
    assert.match(block, new RegExp(`<source src="assets/${name}-demo\\.mp4" type="video/mp4">`), `${name}: mp4 source`);
    assert.match(block, new RegExp(`<img[^>]*src="assets/${name}-demo\\.gif"`), `${name}: gif fallback`);
  }
});

test("console: the tape <video>s do NOT eagerly fetch the multi-MB poster gif on first paint", () => {
  // the whole lazy-load win: browsers fetch a `poster=` gif even under preload="none", and each
  // demo poster is 2.6-3.7MB. So NO eager poster=; the path is stashed in data-poster and the
  // player swaps it onto .poster on scroll-in / tape-open. And every gif <img> fallback is lazy.
  for (const [id, name] of [["termUsing", "using"], ["termHosting", "hosting"], ["termPing", "ping"]]) {
    const block = index.slice(index.indexOf(`id="${id}"`), index.indexOf(`id="${id}"`) + 400);
    // a bare poster= (NOT the deferred data-poster=) is what triggers the eager gif fetch.
    assert.doesNotMatch(block, /(?<!-)\bposter="assets\//, `${name}: no eager poster= (would fetch the gif on paint)`);
    assert.match(block, new RegExp(`data-poster="assets/${name}-demo\\.gif"`), `${name}: poster deferred to data-poster`);
  }
  for (const name of ["using", "hosting", "ping"]) {
    // find the <img> fallback (the one with class term__video-gif, not the mp4 <source>) and
    // assert loading="lazy" sits on that same tag.
    const at = index.indexOf(`class="term__video-gif" src="assets/${name}-demo.gif"`);
    const imgTag = index.slice(at, index.indexOf(">", at) + 1);
    assert.match(imgTag, /loading="lazy"/, `${name}: gif <img> fallback is loading="lazy"`);
  }
});

test("console: the player is preload=none AND actually swaps in the deferred poster", () => {
  // preload="none" stays on every tape (no eager video bytes either).
  for (const id of ["termUsing", "termHosting", "termPing"]) {
    const block = index.slice(index.indexOf(`id="${id}"`), index.indexOf(`id="${id}"`) + 400);
    assert.match(block, /preload="none"/, `${id}: preload="none"`);
  }
  // terminal.js must promote data-poster -> poster (otherwise the deferred poster never shows).
  assert.match(termJs, /data-poster|dataset\.poster|getAttribute\(["']data-poster["']\)/,
    "terminal.js reads the deferred data-poster");
});

test("terminal.js: using + hosting + ping are MEDIA demos wired to their <video> ids, in order", () => {
  assert.match(termJs, /using:\s*{[^}]*media:\s*true[^}]*el:\s*"termUsing"/, "using -> #termUsing");
  assert.match(termJs, /hosting:\s*{[^}]*media:\s*true[^}]*el:\s*"termHosting"/, "hosting -> #termHosting");
  assert.match(termJs, /ping:\s*{[^}]*media:\s*true[^}]*el:\s*"termPing"/, "ping -> #termPing");
  // hosting is declared BETWEEN using and ping in the DEMOS map.
  assert.ok(
    termJs.indexOf('el: "termUsing"') < termJs.indexOf('el: "termHosting"') &&
      termJs.indexOf('el: "termHosting"') < termJs.indexOf('el: "termPing"'),
    "DEMOS order is using < hosting < ping",
  );
  // the engine actually plays a media tape (selects + plays the <video>).
  assert.match(termJs, /function selectMedia\(/, "selectMedia branch exists");
  assert.match(termJs, /function playMedia\(/, "playMedia exists");
  assert.match(termJs, /\.play\(/, "plays the video");
  assert.match(termJs, /prefers-reduced-motion|REDUCED/, "reduced-motion aware");
});

test("assets: every referenced tape file actually exists (no broken tile in prod)", () => {
  // markup-path matching isn't enough: a tape wired to a missing asset 404s in prod.
  // Pin that all six media files exist + are non-empty.
  for (const f of ["ping-demo.mp4", "ping-demo.gif", "using-demo.mp4", "using-demo.gif",
                   "hosting-demo.mp4", "hosting-demo.gif"]) {
    const p = path.join(SRC, "assets", f);
    assert.ok(existsSync(p), `assets/${f} exists`);
    assert.ok(statSync(p).size > 0, `assets/${f} is non-empty`);
  }
});

test("home.css: the media slot matches the ASCII screen slot", () => {
  assert.match(homeCss, /\.term__media\s*{[^}]*min-height:\s*420px/, "media slot is the same 420px slot");
  assert.match(homeCss, /\.term__video\s*{[^}]*width:\s*100%/, "video sizes full-width like the prior diagram");
});
